// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "@openzeppelin/contracts/utils/math/Math.sol";

import "../interfaces/ITradingPair.sol";

/**
 * @title TradingPair
 * @dev AMM 交易对合约（类似 Uniswap V2）
 *
 * 核心概念：
 * - 恒定乘积公式: x * y = k
 * - 添加流动性时按比例存入两种代币，获得 LP Token
 * - 交易时按照 x * y = k 公式计算输出
 * - 手续费 0.3%（30 基点）
 *
 * @notice 这是一个教学和测试用的简化版本
 */
contract TradingPair is ERC20, ReentrancyGuard, ITradingPair {
    using Math for uint256;

    // ============ Constants ============

    uint256 public constant MINIMUM_LIQUIDITY = 1000;
    uint256 private constant FEE_DENOMINATOR = 10000;
    uint256 public constant FEE = 30; // 0.3%

    // ============ State Variables ============

    address public token0;
    address public token1;
    address public factory;

    uint112 private reserve0;
    uint112 private reserve1;
    uint32 private blockTimestampLast;

    uint256 public price0CumulativeLast;
    uint256 public price1CumulativeLast;
    uint256 public kLast; // reserve0 * reserve1, as of immediately after the most recent liquidity event

    // ============ Constructor ============

    constructor() ERC20("Trading LP Token", "TLP") {
        factory = msg.sender;
    }

    // ============ Initialization ============

    /**
     * @dev 初始化交易对
     * @param _token0 第一个代币地址
     * @param _token1 第二个代币地址
     */
    function initialize(address _token0, address _token1) external {
        require(msg.sender == factory, "TradingPair: FORBIDDEN");
        token0 = _token0;
        token1 = _token1;
    }

    // ============ View Functions ============

    /**
     * @dev 获取当前储备量
     */
    function getReserves()
        public
        view
        returns (uint112 _reserve0, uint112 _reserve1, uint32 _blockTimestampLast)
    {
        _reserve0 = reserve0;
        _reserve1 = reserve1;
        _blockTimestampLast = blockTimestampLast;
    }

    // ============ Core Functions ============

    /**
     * @dev 添加流动性，铸造 LP Token
     *
     * 流程：
     * 1. 用户先将代币转入合约
     * 2. 调用 mint 计算并铸造 LP Token
     *
     * @param to LP Token 接收地址
     * @return liquidity 铸造的 LP Token 数量
     */
    function mint(address to) external nonReentrant returns (uint256 liquidity) {
        (uint112 _reserve0, uint112 _reserve1, ) = getReserves();

        // 计算本次存入的代币数量
        uint256 balance0 = IERC20(token0).balanceOf(address(this));
        uint256 balance1 = IERC20(token1).balanceOf(address(this));
        uint256 amount0 = balance0 - _reserve0;
        uint256 amount1 = balance1 - _reserve1;

        uint256 _totalSupply = totalSupply();

        if (_totalSupply == 0) {
            // 首次添加流动性: sqrt(amount0 * amount1) - MINIMUM_LIQUIDITY
            liquidity = Math.sqrt(amount0 * amount1) - MINIMUM_LIQUIDITY;
            // 永久锁定最小流动性，防止价格操纵
            _mint(address(1), MINIMUM_LIQUIDITY);
        } else {
            // 后续添加: 按比例计算
            liquidity = Math.min(
                (amount0 * _totalSupply) / _reserve0,
                (amount1 * _totalSupply) / _reserve1
            );
        }

        require(liquidity > 0, "TradingPair: INSUFFICIENT_LIQUIDITY_MINTED");
        _mint(to, liquidity);

        _update(balance0, balance1, _reserve0, _reserve1);
        kLast = uint256(reserve0) * reserve1;

        emit Mint(msg.sender, amount0, amount1);
    }

    /**
     * @dev 移除流动性，销毁 LP Token
     *
     * 流程：
     * 1. 用户先将 LP Token 转入合约
     * 2. 调用 burn 计算并返还两种代币
     *
     * @param to 代币接收地址
     * @return amount0 返还的 token0 数量
     * @return amount1 返还的 token1 数量
     */
    function burn(address to) external nonReentrant returns (uint256 amount0, uint256 amount1) {
        (uint112 _reserve0, uint112 _reserve1, ) = getReserves();
        address _token0 = token0;
        address _token1 = token1;

        uint256 balance0 = IERC20(_token0).balanceOf(address(this));
        uint256 balance1 = IERC20(_token1).balanceOf(address(this));
        uint256 liquidity = balanceOf(address(this));

        uint256 _totalSupply = totalSupply();
        // 按 LP Token 比例计算返还数量
        amount0 = (liquidity * balance0) / _totalSupply;
        amount1 = (liquidity * balance1) / _totalSupply;

        require(amount0 > 0 && amount1 > 0, "TradingPair: INSUFFICIENT_LIQUIDITY_BURNED");

        _burn(address(this), liquidity);

        _safeTransfer(_token0, to, amount0);
        _safeTransfer(_token1, to, amount1);

        balance0 = IERC20(_token0).balanceOf(address(this));
        balance1 = IERC20(_token1).balanceOf(address(this));

        _update(balance0, balance1, _reserve0, _reserve1);
        kLast = uint256(reserve0) * reserve1;

        emit Burn(msg.sender, amount0, amount1, to);
    }

    /**
     * @dev 执行代币交换
     *
     * 核心逻辑：
     * 1. 验证输出数量有效
     * 2. 转出代币给用户
     * 3. 验证 K 值不变（考虑手续费后）
     *
     * @param amount0Out token0 输出数量
     * @param amount1Out token1 输出数量
     * @param to 接收地址
     * @param data 回调数据（闪电贷用）
     */
    function swap(
        uint256 amount0Out,
        uint256 amount1Out,
        address to,
        bytes calldata data
    ) external nonReentrant {
        require(amount0Out > 0 || amount1Out > 0, "TradingPair: INSUFFICIENT_OUTPUT_AMOUNT");

        (uint112 _reserve0, uint112 _reserve1, ) = getReserves();
        require(
            amount0Out < _reserve0 && amount1Out < _reserve1,
            "TradingPair: INSUFFICIENT_LIQUIDITY"
        );

        uint256 balance0;
        uint256 balance1;
        {
            address _token0 = token0;
            address _token1 = token1;
            require(to != _token0 && to != _token1, "TradingPair: INVALID_TO");

            // 乐观转账
            if (amount0Out > 0) _safeTransfer(_token0, to, amount0Out);
            if (amount1Out > 0) _safeTransfer(_token1, to, amount1Out);

            // 闪电贷回调（可选）
            if (data.length > 0) {
                // ICallee(to).call(msg.sender, amount0Out, amount1Out, data);
            }

            balance0 = IERC20(_token0).balanceOf(address(this));
            balance1 = IERC20(_token1).balanceOf(address(this));
        }

        // 计算输入数量
        uint256 amount0In = balance0 > _reserve0 - amount0Out
            ? balance0 - (_reserve0 - amount0Out)
            : 0;
        uint256 amount1In = balance1 > _reserve1 - amount1Out
            ? balance1 - (_reserve1 - amount1Out)
            : 0;
        require(amount0In > 0 || amount1In > 0, "TradingPair: INSUFFICIENT_INPUT_AMOUNT");

        // 验证 K 值（考虑 0.3% 手续费）
        // (balance0 * 10000 - amount0In * 30) * (balance1 * 10000 - amount1In * 30) >= reserve0 * reserve1 * 10000^2
        {
            uint256 balance0Adjusted = balance0 * FEE_DENOMINATOR - amount0In * FEE;
            uint256 balance1Adjusted = balance1 * FEE_DENOMINATOR - amount1In * FEE;
            require(
                balance0Adjusted * balance1Adjusted >=
                    uint256(_reserve0) * _reserve1 * FEE_DENOMINATOR ** 2,
                "TradingPair: K"
            );
        }

        _update(balance0, balance1, _reserve0, _reserve1);

        emit Swap(msg.sender, amount0In, amount1In, amount0Out, amount1Out, to);
    }

    /**
     * @dev 强制同步余额到储备量
     */
    function sync() external nonReentrant {
        _update(
            IERC20(token0).balanceOf(address(this)),
            IERC20(token1).balanceOf(address(this)),
            reserve0,
            reserve1
        );
    }

    /**
     * @dev 提取多余代币（如果有人误转）
     */
    function skim(address to) external nonReentrant {
        address _token0 = token0;
        address _token1 = token1;
        _safeTransfer(_token0, to, IERC20(_token0).balanceOf(address(this)) - reserve0);
        _safeTransfer(_token1, to, IERC20(_token1).balanceOf(address(this)) - reserve1);
    }

    // ============ Internal Functions ============

    /**
     * @dev 更新储备量和价格累计值
     */
    function _update(
        uint256 balance0,
        uint256 balance1,
        uint112 _reserve0,
        uint112 _reserve1
    ) private {
        require(balance0 <= type(uint112).max && balance1 <= type(uint112).max, "TradingPair: OVERFLOW");

        uint32 blockTimestamp = uint32(block.timestamp % 2 ** 32);
        unchecked {
            uint32 timeElapsed = blockTimestamp - blockTimestampLast;

            if (timeElapsed > 0 && _reserve0 != 0 && _reserve1 != 0) {
                // 更新价格累计值（用于 TWAP 预言机）
                price0CumulativeLast += uint256((uint224(_reserve1) << 112) / _reserve0) * timeElapsed;
                price1CumulativeLast += uint256((uint224(_reserve0) << 112) / _reserve1) * timeElapsed;
            }
        }

        reserve0 = uint112(balance0);
        reserve1 = uint112(balance1);
        blockTimestampLast = blockTimestamp;

        emit Sync(reserve0, reserve1);
    }

    /**
     * @dev 安全转账
     */
    function _safeTransfer(address token, address to, uint256 value) private {
        (bool success, bytes memory data) = token.call(
            abi.encodeWithSelector(IERC20.transfer.selector, to, value)
        );
        require(success && (data.length == 0 || abi.decode(data, (bool))), "TradingPair: TRANSFER_FAILED");
    }
}
