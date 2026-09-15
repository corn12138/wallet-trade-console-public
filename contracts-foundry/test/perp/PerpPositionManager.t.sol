// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { PerpTestBase } from "./PerpTestBase.sol";

contract PerpPositionManagerTest is PerpTestBase {
    event IncreasePosition(
        bytes32 key,
        address account,
        address indexToken,
        address collateralToken,
        uint256 collateralDelta,
        uint256 sizeDelta,
        bool isLong,
        uint256 price,
        uint256 fee
    );

    function test_MarketRejectsOpenFromNonPositionManager() public {
        vm.expectRevert("PerpMarket: FORBIDDEN");
        vm.prank(user1);
        market.openPosition(
            user1, address(weth), address(usdc), _token(100), _usd(1000), true, _price(3450)
        );
    }

    function test_MarketPositionKeyIsDeterministic() public view {
        bytes32 key1 = market.getPositionKey(user1, address(weth), address(usdc), true);
        bytes32 key2 = market.getPositionKey(user1, address(weth), address(usdc), true);

        assertEq(key1, key2);
    }

    function test_MarketPositionKeySeparatesLongAndShort() public view {
        bytes32 longKey = market.getPositionKey(user1, address(weth), address(usdc), true);
        bytes32 shortKey = market.getPositionKey(user1, address(weth), address(usdc), false);

        assertNotEq(longKey, shortKey);
    }

    function test_OpenLongAtFiveTimesLeverage() public {
        uint256 collateral = _token(100);
        uint256 sizeDelta = _usd(500);
        bytes32 key = market.getPositionKey(user1, address(weth), address(usdc), true);
        uint256 fee = (sizeDelta * market.MARGIN_FEE_BASIS_POINTS()) / market.BASIS_POINTS_DIVISOR();

        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectEmit(false, false, false, true, address(market));
        emit IncreasePosition(
            key, user1, address(weth), address(usdc), collateral, sizeDelta, true, _price(3450), fee
        );
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, sizeDelta, true, 0, deadline
        );

        assertEq(_positionSize(user1, true), sizeDelta);
        assertEq(_positionAveragePrice(user1, true), _price(3450));
    }

    function test_OpenShortPosition() public {
        _open(user1, _token(200), _usd(2000), false);

        assertEq(_positionSize(user1, false), _usd(2000));
    }

    function test_OpenRejectsLeverageBelowOneTimes() public {
        uint256 collateral = _token(1000);
        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectRevert("PerpMarket: LEVERAGE_TOO_LOW");
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, _usd(500), true, 0, deadline
        );
    }

    function test_OpenRejectsLeverageAboveFiftyTimes() public {
        uint256 collateral = _token(10);
        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectRevert("PerpMarket: MAX_LEVERAGE_EXCEEDED");
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, _usd(600), true, 0, deadline
        );
    }

    function test_OpenRejectsExpiredDeadline() public {
        uint256 collateral = _token(100);
        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectRevert("PositionManager: EXPIRED");
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, _usd(500), true, 0, block.timestamp - 1
        );
    }

    function test_OpenLongRejectsPriceAboveLimit() public {
        uint256 collateral = _token(100);
        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectRevert("PositionManager: PRICE_TOO_HIGH");
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, _usd(500), true, _price(3000), deadline
        );
    }

    function test_OpenShortRejectsPriceBelowLimit() public {
        uint256 collateral = _token(100);
        vm.prank(user1);
        usdc.approve(address(positionManager), collateral);

        vm.expectRevert("PositionManager: PRICE_TOO_LOW");
        vm.prank(user1);
        positionManager.openPosition(
            address(weth), address(usdc), collateral, _usd(500), false, _price(4000), deadline
        );
    }

    function test_CloseProfitableLongDeletesPosition() public {
        uint256 collateral = _token(1000);
        uint256 sizeDelta = _usd(5000);
        _open(user1, collateral, sizeDelta, true);
        oracle.setManualPrice(address(weth), _price(3800));
        uint256 balanceBefore = usdc.balanceOf(user1);

        _close(user1, sizeDelta, true);

        assertGt(usdc.balanceOf(user1), balanceBefore);
        assertEq(_positionSize(user1, true), 0);
    }

    function test_CloseLosingLongReturnsReducedCollateral() public {
        uint256 collateral = _token(1000);
        uint256 sizeDelta = _usd(5000);
        _open(user1, collateral, sizeDelta, true);
        oracle.setManualPrice(address(weth), _price(3200));
        uint256 balanceBefore = usdc.balanceOf(user1);

        _close(user1, sizeDelta, true);

        uint256 received = usdc.balanceOf(user1) - balanceBefore;
        assertLt(received, collateral);
        assertGt(received, 0);
    }

    function test_CloseProfitableShortReturnsMoreThanCollateral() public {
        uint256 collateral = _token(1000);
        uint256 sizeDelta = _usd(5000);
        _open(user1, collateral, sizeDelta, false);
        oracle.setManualPrice(address(weth), _price(3000));
        uint256 balanceBefore = usdc.balanceOf(user1);

        _close(user1, sizeDelta, false);

        assertGt(usdc.balanceOf(user1), balanceBefore + collateral);
    }

    function test_CloseCanReducePositionPartially() public {
        _open(user1, _token(1000), _usd(5000), true);

        _close(user1, _usd(2500), true);

        assertEq(_positionSize(user1, true), _usd(2500));
    }

    function test_CloseRejectsMissingPosition() public {
        vm.expectRevert("PerpMarket: NO_POSITION");
        vm.prank(user1);
        positionManager.closePosition(
            address(weth), address(usdc), 0, _usd(1000), true, 0, deadline
        );
    }

    function test_OpenAndCloseCollectPointOnePercentFeeEach() public {
        uint256 feeBalanceBefore = usdc.balanceOf(feeReceiver);
        uint256 sizeDelta = _usd(5000);

        _open(user1, _token(1000), sizeDelta, true);
        _close(user1, sizeDelta, true);

        assertEq(usdc.balanceOf(feeReceiver) - feeBalanceBefore, _token(10));
    }
}
