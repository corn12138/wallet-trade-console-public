// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { Test } from "forge-std/Test.sol";
import { PerpMarket } from "../../src/core/PerpMarket.sol";
import { PerpOracle } from "../../src/core/PerpOracle.sol";
import { PerpVault } from "../../src/core/PerpVault.sol";
import { PositionManager } from "../../src/periphery/PositionManager.sol";
import { MockERC20 } from "../../src/tokens/MockERC20.sol";

abstract contract PerpTestBase is Test {
    uint256 internal constant PRICE_PRECISION = 10 ** 30;

    address internal user1 = makeAddr("user1");
    address internal user2 = makeAddr("user2");
    address internal feeReceiver = makeAddr("feeReceiver");
    address internal liquidator = makeAddr("liquidator");

    MockERC20 internal usdc;
    MockERC20 internal weth;
    PerpOracle internal oracle;
    PerpVault internal vault;
    PerpMarket internal market;
    PositionManager internal positionManager;
    uint256 internal deadline;

    function setUp() public virtual {
        vm.warp(1 days);

        usdc = new MockERC20("Mock USDC", "USDC", 18);
        weth = new MockERC20("Mock WETH", "WETH", 18);
        oracle = new PerpOracle();
        vault = new PerpVault();
        market = new PerpMarket(address(vault), address(oracle), feeReceiver);
        positionManager = new PositionManager(address(market), address(oracle));

        vault.setMarket(address(market));
        market.setPositionManager(address(positionManager));

        oracle.setManualPrice(address(weth), _price(3450));
        oracle.setManualPrice(address(usdc), _price(1));

        usdc.mint(address(this), _token(500_000));
        weth.mint(address(this), _token(500));
        usdc.approve(address(vault), type(uint256).max);
        weth.approve(address(vault), type(uint256).max);
        vault.addLiquidity(address(usdc), _token(500_000));
        vault.addLiquidity(address(weth), _token(500));

        usdc.mint(user1, _token(100_000));
        usdc.mint(user2, _token(100_000));
        weth.mint(user1, _token(100));
        weth.mint(user2, _token(100));

        deadline = block.timestamp + 1 hours;
    }

    function _open(address account, uint256 collateral, uint256 sizeDelta, bool isLong) internal {
        vm.startPrank(account);
        usdc.approve(address(positionManager), collateral);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, sizeDelta, isLong, 0, deadline
        );
        vm.stopPrank();
    }

    function _close(address account, uint256 sizeDelta, bool isLong) internal {
        vm.prank(account);
        positionManager.closePosition(
            address(weth), address(usdc), 0, sizeDelta, isLong, 0, deadline
        );
    }

    function _positionSize(address account, bool isLong) internal view returns (uint256 size) {
        bytes32 key = market.getPositionKey(account, address(weth), address(usdc), isLong);
        (size,,,,,,) = market.positions(key);
    }

    function _positionAveragePrice(
        address account,
        bool isLong
    )
        internal
        view
        returns (uint256 averagePrice)
    {
        bytes32 key = market.getPositionKey(account, address(weth), address(usdc), isLong);
        (,, averagePrice,,,,) = market.positions(key);
    }

    function _price(uint256 amount) internal pure returns (uint256) {
        return amount * PRICE_PRECISION;
    }

    function _usd(uint256 amount) internal pure returns (uint256) {
        return amount * PRICE_PRECISION;
    }

    function _token(uint256 amount) internal pure returns (uint256) {
        return amount * 1 ether;
    }
}
