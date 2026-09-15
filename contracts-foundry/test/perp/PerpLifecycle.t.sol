// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { SafeCast } from "@openzeppelin/contracts/utils/math/SafeCast.sol";
import { PerpTestBase } from "./PerpTestBase.sol";

contract PerpLifecycleTest is PerpTestBase {
    event LiquidatePosition(
        bytes32 key,
        address account,
        address indexToken,
        bool isLong,
        uint256 size,
        uint256 collateral,
        int256 pnl
    );

    function test_LiquidatesUnderwaterPositionAndRewardsLiquidator() public {
        uint256 sizeDelta = _usd(2000);
        _open(user1, _token(200), sizeDelta, true);
        oracle.setManualPrice(address(weth), _price(3120));
        uint256 liquidatorBalanceBefore = usdc.balanceOf(liquidator);
        bytes32 key = market.getPositionKey(user1, address(weth), address(usdc), true);
        uint256 openFee =
            (sizeDelta * market.MARGIN_FEE_BASIS_POINTS()) / market.BASIS_POINTS_DIVISOR();
        uint256 positionCollateral = _usd(200) - openFee;
        uint256 loss = ((_price(3450) - _price(3120)) * sizeDelta) / _price(3450);

        vm.expectEmit(false, false, false, true, address(market));
        emit LiquidatePosition(
            key, user1, address(weth), true, sizeDelta, positionCollateral, -SafeCast.toInt256(loss)
        );
        vm.prank(liquidator);
        market.liquidatePosition(user1, address(weth), address(usdc), true, liquidator);

        assertGt(usdc.balanceOf(liquidator), liquidatorBalanceBefore);
        assertEq(_positionSize(user1, true), 0);
    }

    function testRejectsLiquidationOfHealthyPosition() public {
        _open(user1, _token(1000), _usd(2000), true);

        vm.expectRevert("PerpMarket: NOT_LIQUIDATABLE");
        vm.prank(liquidator);
        market.liquidatePosition(user1, address(weth), address(usdc), true, liquidator);
    }

    function test_LifecycleSupportsPartialThenFullCloseAcrossPriceMoves() public {
        _open(user1, _token(1000), _usd(10_000), true);
        oracle.setManualPrice(address(weth), _price(3600));
        uint256 balanceBeforePartialClose = usdc.balanceOf(user1);

        _close(user1, _usd(5000), true);

        assertGt(usdc.balanceOf(user1) - balanceBeforePartialClose, 0);
        assertEq(_positionSize(user1, true), _usd(5000));

        oracle.setManualPrice(address(weth), _price(3450));
        _close(user1, _usd(5000), true);

        assertEq(_positionSize(user1, true), 0);
    }

    function test_MultipleUsersTrackIndependentPositionsAndOpenInterest() public {
        uint256 collateral = _token(500);
        uint256 sizeDelta = _usd(2500);
        _open(user1, collateral, sizeDelta, true);
        _open(user2, collateral, sizeDelta, false);

        assertEq(_positionSize(user1, true), sizeDelta);
        assertEq(_positionSize(user2, false), sizeDelta);
        assertEq(market.globalLongSize(), sizeDelta);
        assertEq(market.globalShortSize(), sizeDelta);

        oracle.setManualPrice(address(weth), _price(3700));
        uint256 user1BalanceBefore = usdc.balanceOf(user1);
        uint256 user2BalanceBefore = usdc.balanceOf(user2);

        _close(user1, sizeDelta, true);
        _close(user2, sizeDelta, false);

        assertGt(usdc.balanceOf(user1) - user1BalanceBefore, collateral);
        assertLt(usdc.balanceOf(user2) - user2BalanceBefore, collateral);
    }

    function test_VaultPoolDoesNotDecreaseForRoundTripAtEntryPrice() public {
        uint256 poolBefore = vault.poolAmounts(address(usdc));
        uint256 sizeDelta = _usd(5000);

        _open(user1, _token(1000), sizeDelta, true);
        _close(user1, sizeDelta, true);

        assertGe(vault.poolAmounts(address(usdc)), poolBefore);
    }
}
