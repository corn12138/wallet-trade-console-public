// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "./TradingPair.sol";
import "../interfaces/ITradingPair.sol";

/**
 * @title TradingPairFactory
 * @dev 交易对工厂合约
 *
 * 功能：
 * - 创建新的交易对
 * - 使用 CREATE2 确定性部署（地址可预测）
 * - 管理手续费接收地址
 */
contract TradingPairFactory is ITradingPairFactory {
    // ============ State Variables ============

    /// @notice 手续费接收地址
    address public feeTo;

    /// @notice 有权修改 feeTo 的地址
    address public feeToSetter;

    /// @notice tokenA => tokenB => pair address
    mapping(address => mapping(address => address)) public getPair;

    /// @notice 所有交易对地址
    address[] public allPairs;

    // ============ Constructor ============

    constructor(address _feeToSetter) {
        feeToSetter = _feeToSetter;
    }

    // ============ View Functions ============

    /**
     * @dev 获取所有交易对数量
     */
    function allPairsLength() external view returns (uint256) {
        return allPairs.length;
    }

    /**
     * @dev 计算交易对地址（不需要部署即可知道）
     * @param tokenA 第一个代币
     * @param tokenB 第二个代币
     */
    function pairFor(address tokenA, address tokenB) public view returns (address pair) {
        (address token0, address token1) = tokenA < tokenB
            ? (tokenA, tokenB)
            : (tokenB, tokenA);

        pair = address(
            uint160(
                uint256(
                    keccak256(
                        abi.encodePacked(
                            hex"ff",
                            address(this),
                            keccak256(abi.encodePacked(token0, token1)),
                            keccak256(type(TradingPair).creationCode)
                        )
                    )
                )
            )
        );
    }

    // ============ State-Changing Functions ============

    /**
     * @dev 创建新的交易对
     * @param tokenA 第一个代币地址
     * @param tokenB 第二个代币地址
     * @return pair 新创建的交易对地址
     */
    function createPair(address tokenA, address tokenB) external returns (address pair) {
        require(tokenA != tokenB, "TradingPairFactory: IDENTICAL_ADDRESSES");

        // 排序代币地址，确保 token0 < token1
        (address token0, address token1) = tokenA < tokenB
            ? (tokenA, tokenB)
            : (tokenB, tokenA);

        require(token0 != address(0), "TradingPairFactory: ZERO_ADDRESS");
        require(getPair[token0][token1] == address(0), "TradingPairFactory: PAIR_EXISTS");

        // 使用 CREATE2 部署（确定性地址）
        bytes memory bytecode = type(TradingPair).creationCode;
        bytes32 salt = keccak256(abi.encodePacked(token0, token1));

        assembly {
            pair := create2(0, add(bytecode, 32), mload(bytecode), salt)
        }

        // 初始化交易对
        TradingPair(pair).initialize(token0, token1);

        // 双向映射
        getPair[token0][token1] = pair;
        getPair[token1][token0] = pair;
        allPairs.push(pair);

        emit PairCreated(token0, token1, pair, allPairs.length);
    }

    /**
     * @dev 设置手续费接收地址
     */
    function setFeeTo(address _feeTo) external {
        require(msg.sender == feeToSetter, "TradingPairFactory: FORBIDDEN");
        feeTo = _feeTo;
    }

    /**
     * @dev 转移 feeToSetter 权限
     */
    function setFeeToSetter(address _feeToSetter) external {
        require(msg.sender == feeToSetter, "TradingPairFactory: FORBIDDEN");
        feeToSetter = _feeToSetter;
    }
}
