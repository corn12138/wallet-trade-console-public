// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "../interfaces/ITradingPair.sol";

/**
 * @title TradingLibrary
 * @dev 交易计算库
 *
 * 核心功能：
 * - 计算交换输出数量
 * - 计算交换输入数量
 * - 获取储备量
 * - 计算交易路径
 */
library TradingLibrary {
    /**
     * @dev 根据输入计算输出
     * 公式: amountOut = (amountIn * 997 * reserveOut) / (reserveIn * 1000 + amountIn * 997)
     *
     * @param amountIn 输入数量
     * @param reserveIn 输入代币储备
     * @param reserveOut 输出代币储备
     * @return amountOut 输出数量
     */
    function getAmountOut(
        uint256 amountIn,
        uint256 reserveIn,
        uint256 reserveOut
    ) internal pure returns (uint256 amountOut) {
        require(amountIn > 0, "TradingLibrary: INSUFFICIENT_INPUT_AMOUNT");
        require(reserveIn > 0 && reserveOut > 0, "TradingLibrary: INSUFFICIENT_LIQUIDITY");

        uint256 amountInWithFee = amountIn * 997;
        uint256 numerator = amountInWithFee * reserveOut;
        uint256 denominator = reserveIn * 1000 + amountInWithFee;
        amountOut = numerator / denominator;
    }

    /**
     * @dev 根据输出计算所需输入
     * 公式: amountIn = (reserveIn * amountOut * 1000) / ((reserveOut - amountOut) * 997) + 1
     *
     * @param amountOut 期望输出数量
     * @param reserveIn 输入代币储备
     * @param reserveOut 输出代币储备
     * @return amountIn 所需输入数量
     */
    function getAmountIn(
        uint256 amountOut,
        uint256 reserveIn,
        uint256 reserveOut
    ) internal pure returns (uint256 amountIn) {
        require(amountOut > 0, "TradingLibrary: INSUFFICIENT_OUTPUT_AMOUNT");
        require(reserveIn > 0 && reserveOut > 0, "TradingLibrary: INSUFFICIENT_LIQUIDITY");
        require(amountOut < reserveOut, "TradingLibrary: INSUFFICIENT_LIQUIDITY");

        uint256 numerator = reserveIn * amountOut * 1000;
        uint256 denominator = (reserveOut - amountOut) * 997;
        amountIn = (numerator / denominator) + 1;
    }

    /**
     * @dev 计算多跳路径的输出数组
     * @param factory 工厂合约地址
     * @param amountIn 初始输入
     * @param path 代币路径
     * @return amounts 每一跳的数量
     */
    function getAmountsOut(
        address factory,
        uint256 amountIn,
        address[] memory path
    ) internal view returns (uint256[] memory amounts) {
        require(path.length >= 2, "TradingLibrary: INVALID_PATH");
        amounts = new uint256[](path.length);
        amounts[0] = amountIn;

        for (uint256 i; i < path.length - 1; i++) {
            (uint256 reserveIn, uint256 reserveOut) = getReserves(factory, path[i], path[i + 1]);
            amounts[i + 1] = getAmountOut(amounts[i], reserveIn, reserveOut);
        }
    }

    /**
     * @dev 计算多跳路径的输入数组
     * @param factory 工厂合约地址
     * @param amountOut 期望最终输出
     * @param path 代币路径
     * @return amounts 每一跳的数量
     */
    function getAmountsIn(
        address factory,
        uint256 amountOut,
        address[] memory path
    ) internal view returns (uint256[] memory amounts) {
        require(path.length >= 2, "TradingLibrary: INVALID_PATH");
        amounts = new uint256[](path.length);
        amounts[amounts.length - 1] = amountOut;

        for (uint256 i = path.length - 1; i > 0; i--) {
            (uint256 reserveIn, uint256 reserveOut) = getReserves(factory, path[i - 1], path[i]);
            amounts[i - 1] = getAmountIn(amounts[i], reserveIn, reserveOut);
        }
    }

    /**
     * @dev 获取交易对储备量（按代币地址排序）
     */
    function getReserves(
        address factory,
        address tokenA,
        address tokenB
    ) internal view returns (uint256 reserveA, uint256 reserveB) {
        (address token0, ) = sortTokens(tokenA, tokenB);
        address pair = ITradingPairFactory(factory).getPair(tokenA, tokenB);
        require(pair != address(0), "TradingLibrary: PAIR_NOT_FOUND");

        (uint112 reserve0, uint112 reserve1, ) = ITradingPair(pair).getReserves();
        (reserveA, reserveB) = tokenA == token0
            ? (uint256(reserve0), uint256(reserve1))
            : (uint256(reserve1), uint256(reserve0));
    }

    /**
     * @dev 排序代币地址
     */
    function sortTokens(
        address tokenA,
        address tokenB
    ) internal pure returns (address token0, address token1) {
        require(tokenA != tokenB, "TradingLibrary: IDENTICAL_ADDRESSES");
        (token0, token1) = tokenA < tokenB ? (tokenA, tokenB) : (tokenB, tokenA);
        require(token0 != address(0), "TradingLibrary: ZERO_ADDRESS");
    }

    /**
     * @dev 计算滑点百分比
     * @param expectedAmount 预期输出
     * @param actualAmount 实际输出
     * @return slippage 滑点（基点，10000 = 100%）
     */
    function calculateSlippage(
        uint256 expectedAmount,
        uint256 actualAmount
    ) internal pure returns (uint256 slippage) {
        if (actualAmount >= expectedAmount) {
            return 0;
        }
        slippage = ((expectedAmount - actualAmount) * 10000) / expectedAmount;
    }

    /**
     * @dev 计算价格影响
     * @param amountIn 输入数量
     * @param reserveIn 输入储备
     * @param reserveOut 输出储备
     * @return priceImpact 价格影响（基点）
     */
    function calculatePriceImpact(
        uint256 amountIn,
        uint256 reserveIn,
        uint256 reserveOut
    ) internal pure returns (uint256 priceImpact) {
        // 理想价格（无滑点）
        uint256 idealOut = (amountIn * reserveOut) / reserveIn;
        // 实际输出
        uint256 actualOut = getAmountOut(amountIn, reserveIn, reserveOut);

        if (actualOut >= idealOut) {
            return 0;
        }
        priceImpact = ((idealOut - actualOut) * 10000) / idealOut;
    }
}
