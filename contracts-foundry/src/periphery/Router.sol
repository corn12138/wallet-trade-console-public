// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import "../interfaces/ITradingPair.sol";
import "./TradingLibrary.sol";

/**
 * @title Router
 * @dev 交易路由合约
 *
 * 功能：
 * - 简化代币交换操作
 * - 支持多跳路由
 * - 添加/移除流动性
 *
 * @notice 用户通过 Router 交互，而不是直接调用 Pair
 */
contract Router is ReentrancyGuard {
    using SafeERC20 for IERC20;

    // ============ State Variables ============

    address public immutable factory;
    address public immutable WETH;

    // ============ Modifiers ============

    modifier ensure(uint256 deadline) {
        require(deadline >= block.timestamp, "Router: EXPIRED");
        _;
    }

    // ============ Constructor ============

    constructor(address _factory, address _WETH) {
        factory = _factory;
        WETH = _WETH;
    }

    // ============ Swap Functions ============

    /**
     * @dev 精确输入交换
     * @param amountIn 输入数量
     * @param amountOutMin 最小输出（滑点保护）
     * @param path 交换路径
     * @param to 接收地址
     * @param deadline 截止时间
     * @return amounts 每一跳的数量
     */
    function swapExactTokensForTokens(
        uint256 amountIn,
        uint256 amountOutMin,
        address[] calldata path,
        address to,
        uint256 deadline
    ) external nonReentrant ensure(deadline) returns (uint256[] memory amounts) {
        amounts = TradingLibrary.getAmountsOut(factory, amountIn, path);
        require(amounts[amounts.length - 1] >= amountOutMin, "Router: INSUFFICIENT_OUTPUT_AMOUNT");

        // 转入第一个代币到交易对
        IERC20(path[0]).safeTransferFrom(
            msg.sender,
            ITradingPairFactory(factory).getPair(path[0], path[1]),
            amounts[0]
        );

        // 执行交换
        _swap(amounts, path, to);
    }

    /**
     * @dev 精确输出交换
     * @param amountOut 期望输出数量
     * @param amountInMax 最大输入（滑点保护）
     * @param path 交换路径
     * @param to 接收地址
     * @param deadline 截止时间
     * @return amounts 每一跳的数量
     */
    function swapTokensForExactTokens(
        uint256 amountOut,
        uint256 amountInMax,
        address[] calldata path,
        address to,
        uint256 deadline
    ) external nonReentrant ensure(deadline) returns (uint256[] memory amounts) {
        amounts = TradingLibrary.getAmountsIn(factory, amountOut, path);
        require(amounts[0] <= amountInMax, "Router: EXCESSIVE_INPUT_AMOUNT");

        IERC20(path[0]).safeTransferFrom(
            msg.sender,
            ITradingPairFactory(factory).getPair(path[0], path[1]),
            amounts[0]
        );

        _swap(amounts, path, to);
    }

    // ============ Liquidity Functions ============

    /**
     * @dev 添加流动性
     */
    function addLiquidity(
        address tokenA,
        address tokenB,
        uint256 amountADesired,
        uint256 amountBDesired,
        uint256 amountAMin,
        uint256 amountBMin,
        address to,
        uint256 deadline
    )
        external
        nonReentrant
        ensure(deadline)
        returns (uint256 amountA, uint256 amountB, uint256 liquidity)
    {
        (amountA, amountB) = _calculateLiquidityAmounts(
            tokenA, tokenB, amountADesired, amountBDesired, amountAMin, amountBMin
        );
        liquidity = _addLiquidity(tokenA, tokenB, amountA, amountB, to);
    }

    function _addLiquidity(
        address tokenA,
        address tokenB,
        uint256 amountA,
        uint256 amountB,
        address to
    ) internal returns (uint256 liquidity) {
        address pair = ITradingPairFactory(factory).getPair(tokenA, tokenB);
        if (pair == address(0)) {
            pair = ITradingPairFactory(factory).createPair(tokenA, tokenB);
        }
        IERC20(tokenA).safeTransferFrom(msg.sender, pair, amountA);
        IERC20(tokenB).safeTransferFrom(msg.sender, pair, amountB);
        liquidity = ITradingPair(pair).mint(to);
    }

    /**
     * @dev 移除流动性
     * @param tokenA 代币A地址
     * @param tokenB 代币B地址
     * @param liquidity LP Token 数量
     * @param amountAMin 最小A返还
     * @param amountBMin 最小B返还
     * @param to 接收地址
     * @param deadline 截止时间
     */
    function removeLiquidity(
        address tokenA,
        address tokenB,
        uint256 liquidity,
        uint256 amountAMin,
        uint256 amountBMin,
        address to,
        uint256 deadline
    ) external nonReentrant ensure(deadline) returns (uint256 amountA, uint256 amountB) {
        address pair = ITradingPairFactory(factory).getPair(tokenA, tokenB);
        require(pair != address(0), "Router: PAIR_NOT_FOUND");

        // 转入 LP Token
        IERC20(pair).safeTransferFrom(msg.sender, pair, liquidity);

        // 销毁并获取代币
        (uint256 amount0, uint256 amount1) = ITradingPair(pair).burn(to);

        // 排序返回
        (address token0, ) = TradingLibrary.sortTokens(tokenA, tokenB);
        (amountA, amountB) = tokenA == token0 ? (amount0, amount1) : (amount1, amount0);

        require(amountA >= amountAMin, "Router: INSUFFICIENT_A_AMOUNT");
        require(amountB >= amountBMin, "Router: INSUFFICIENT_B_AMOUNT");
    }

    // ============ View Functions ============

    /**
     * @dev 获取预期输出数量
     */
    function getAmountsOut(
        uint256 amountIn,
        address[] calldata path
    ) external view returns (uint256[] memory amounts) {
        return TradingLibrary.getAmountsOut(factory, amountIn, path);
    }

    /**
     * @dev 获取所需输入数量
     */
    function getAmountsIn(
        uint256 amountOut,
        address[] calldata path
    ) external view returns (uint256[] memory amounts) {
        return TradingLibrary.getAmountsIn(factory, amountOut, path);
    }

    /**
     * @dev 获取交易对储备量
     */
    function getReserves(
        address tokenA,
        address tokenB
    ) external view returns (uint256 reserveA, uint256 reserveB) {
        return TradingLibrary.getReserves(factory, tokenA, tokenB);
    }

    /**
     * @dev 计算价格影响
     */
    function getPriceImpact(
        uint256 amountIn,
        address tokenIn,
        address tokenOut
    ) external view returns (uint256 priceImpact) {
        (uint256 reserveIn, uint256 reserveOut) = TradingLibrary.getReserves(
            factory,
            tokenIn,
            tokenOut
        );
        return TradingLibrary.calculatePriceImpact(amountIn, reserveIn, reserveOut);
    }

    // ============ Internal Functions ============

    /**
     * @dev 执行多跳交换
     */
    function _swap(
        uint256[] memory amounts,
        address[] memory path,
        address _to
    ) internal {
        for (uint256 i; i < path.length - 1; i++) {
            (address input, address output) = (path[i], path[i + 1]);
            (address token0, ) = TradingLibrary.sortTokens(input, output);

            uint256 amountOut = amounts[i + 1];
            (uint256 amount0Out, uint256 amount1Out) = input == token0
                ? (uint256(0), amountOut)
                : (amountOut, uint256(0));

            // 最后一跳发送到目标地址，否则发送到下一个交易对
            address to = i < path.length - 2
                ? ITradingPairFactory(factory).getPair(output, path[i + 2])
                : _to;

            ITradingPair(ITradingPairFactory(factory).getPair(input, output)).swap(
                amount0Out,
                amount1Out,
                to,
                new bytes(0)
            );
        }
    }

    /**
     * @dev 计算最优流动性数量
     */
    function _calculateLiquidityAmounts(
        address tokenA,
        address tokenB,
        uint256 amountADesired,
        uint256 amountBDesired,
        uint256 amountAMin,
        uint256 amountBMin
    ) internal view returns (uint256 amountA, uint256 amountB) {
        address pair = ITradingPairFactory(factory).getPair(tokenA, tokenB);

        // 新交易对，按期望数量添加
        if (pair == address(0)) {
            return (amountADesired, amountBDesired);
        }

        (uint256 reserveA, uint256 reserveB) = TradingLibrary.getReserves(factory, tokenA, tokenB);

        // 空池子
        if (reserveA == 0 && reserveB == 0) {
            return (amountADesired, amountBDesired);
        }

        // 按比例计算
        uint256 amountBOptimal = (amountADesired * reserveB) / reserveA;
        if (amountBOptimal <= amountBDesired) {
            require(amountBOptimal >= amountBMin, "Router: INSUFFICIENT_B_AMOUNT");
            return (amountADesired, amountBOptimal);
        }

        uint256 amountAOptimal = (amountBDesired * reserveA) / reserveB;
        require(amountAOptimal <= amountADesired && amountAOptimal >= amountAMin, "Router: INSUFFICIENT_A_AMOUNT");
        return (amountAOptimal, amountBDesired);
    }
}
