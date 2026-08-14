// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import "../interfaces/IPerpMarket.sol";
import "../core/PerpOracle.sol";

contract PositionManager is ReentrancyGuard {
    using SafeERC20 for IERC20;

    address public immutable market;
    address public immutable oracle;

    modifier ensure(uint256 deadline) {
        require(deadline >= block.timestamp, "PositionManager: EXPIRED");
        _;
    }

    constructor(address _market, address _oracle) {
        market = _market;
        oracle = _oracle;
    }

    /**
     * @param acceptablePrice Max price for longs, min price for shorts (0 = no limit)
     * @param deadline Transaction deadline timestamp
     */
    function openPosition(
        address indexToken,
        address collateralToken,
        uint256 collateralAmount,
        uint256 sizeDelta,
        bool isLong,
        uint256 acceptablePrice,
        uint256 deadline
    ) external nonReentrant ensure(deadline) {
        // 1. Get execution price first (before any transfers)
        uint256 price = PerpOracle(oracle).getPrice(indexToken);

        // 2. Validate slippage
        if (acceptablePrice > 0) {
            if (isLong) {
                require(price <= acceptablePrice, "PositionManager: PRICE_TOO_HIGH");
            } else {
                require(price >= acceptablePrice, "PositionManager: PRICE_TOO_LOW");
            }
        }

        // 3. Transfer collateral from user to Market
        if (collateralAmount > 0) {
            IERC20(collateralToken).safeTransferFrom(
                msg.sender,
                market,
                collateralAmount
            );
        }

        // 4. Call Market
        IPerpMarket(market).openPosition(
            msg.sender,
            indexToken,
            collateralToken,
            collateralAmount,
            sizeDelta,
            isLong,
            price
        );
    }

    /**
     * @param acceptablePrice Min price for longs, max price for shorts (0 = no limit)
     * @param deadline Transaction deadline timestamp
     */
    function closePosition(
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 acceptablePrice,
        uint256 deadline
    ) external nonReentrant ensure(deadline) {
        uint256 price = PerpOracle(oracle).getPrice(indexToken);

        // Validate slippage for closing
        if (acceptablePrice > 0) {
            if (isLong) {
                require(price >= acceptablePrice, "PositionManager: PRICE_TOO_LOW");
            } else {
                require(price <= acceptablePrice, "PositionManager: PRICE_TOO_HIGH");
            }
        }

        IPerpMarket(market).closePosition(
            msg.sender,
            indexToken,
            collateralToken,
            collateralDelta,
            sizeDelta,
            isLong,
            price
        );
    }
}
