// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { PerpTestBase } from "./PerpTestBase.sol";

contract PerpOracleVaultTest is PerpTestBase {
    event ManualPriceSet(address indexed token, uint256 price);

    function test_OracleReturnsManualPrice() public view {
        assertEq(oracle.getPrice(address(weth)), _price(3450));
    }

    function test_OracleRejectsUnsetTokenPrice() public {
        vm.expectRevert("PerpOracle: INVALID_PRICE");
        oracle.getPrice(makeAddr("unsetToken"));
    }

    function test_OracleOwnerCanUpdateManualPrice() public {
        oracle.setManualPrice(address(weth), _price(4000));

        assertEq(oracle.getPrice(address(weth)), _price(4000));
    }

    function test_OracleRejectsNonOwnerManualPriceUpdate() public {
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user1));
        vm.prank(user1);
        oracle.setManualPrice(address(weth), _price(999));
    }

    function test_OracleEmitsManualPriceSet() public {
        vm.expectEmit(true, false, false, true, address(oracle));
        emit ManualPriceSet(address(weth), _price(5000));

        oracle.setManualPrice(address(weth), _price(5000));
    }

    function test_OracleOwnerCanToggleManualMode() public {
        assertTrue(oracle.isManualMode());

        oracle.setManualMode(false);

        assertFalse(oracle.isManualMode());
    }

    function testVaultTracksPoolAfterAddingLiquidity() public view {
        assertEq(vault.poolAmounts(address(usdc)), _token(500_000));
    }

    function test_VaultOwnerCanRemoveLiquidity() public {
        uint256 balanceBefore = usdc.balanceOf(address(this));

        vault.removeLiquidity(address(usdc), _token(1000), address(this));

        assertEq(usdc.balanceOf(address(this)) - balanceBefore, _token(1000));
        assertEq(vault.poolAmounts(address(usdc)), _token(499_000));
    }

    function test_VaultRejectsNonOwnerLiquidityRemoval() public {
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user1));
        vm.prank(user1);
        vault.removeLiquidity(address(usdc), _token(1), user1);
    }

    function test_VaultRejectsNonMarketDepositAndWithdraw() public {
        vm.startPrank(user1);

        vm.expectRevert("PerpVault: FORBIDDEN");
        vault.deposit(address(usdc), _token(100));

        vm.expectRevert("PerpVault: FORBIDDEN");
        vault.withdraw(address(usdc), _token(100), user1);

        vm.stopPrank();
    }

    function test_VaultRejectsLiquidityRemovalBeyondPoolAmount() public {
        vm.expectRevert("PerpVault: INSUFFICIENT_POOL");
        vault.removeLiquidity(address(usdc), _token(600_000), address(this));
    }

    function test_VaultRejectsLiquidityRemovalBeyondUnreservedAmount() public {
        _open(user1, _token(1000), _usd(5000), false);
        uint256 poolAmount = vault.poolAmounts(address(usdc));
        uint256 reservedAmount = vault.reservedAmounts(address(usdc));
        assertGt(reservedAmount, 0);

        vm.expectRevert("PerpVault: RESERVED");
        vault.removeLiquidity(address(usdc), poolAmount - reservedAmount + 1, address(this));
    }
}
