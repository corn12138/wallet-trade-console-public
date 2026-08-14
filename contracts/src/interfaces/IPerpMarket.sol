// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IPerpMarket {
    function openPosition(
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 price
    ) external;

    function closePosition(
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 price
    ) external;

    function liquidatePosition(
        address account,
        address indexToken,
        address collateralToken,
        bool isLong,
        address feeReceiver
    ) external;
}
