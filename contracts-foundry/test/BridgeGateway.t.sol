// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {IAccessControl} from "@openzeppelin/contracts/access/IAccessControl.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {BridgeGateway} from "../src/bridge/BridgeGateway.sol";
import {MockERC20} from "../src/tokens/MockERC20.sol";

/// Tests for the lock-and-release gateway.
///
/// Two gateways are deployed to model the two chains of a route. Solidity tests
/// cannot fork two chains at once, so the "source" and "destination" gateways
/// live in one EVM and the relayer hop is performed explicitly — which is
/// exactly the boundary the off-chain relayer crosses in production.
///
/// The properties pinned here are the ones the trust model depends on:
/// replay protection, route gating, liquidity honesty, and access control.
contract BridgeGatewayTest is Test {
    BridgeGateway internal src;
    BridgeGateway internal dst;
    MockERC20 internal srcToken;
    MockERC20 internal dstToken;

    address internal admin = address(this);
    address internal relayer = makeAddr("relayer");
    address internal alice = makeAddr("alice");
    address internal bob = makeAddr("bob");
    address internal attacker = makeAddr("attacker");

    uint256 internal constant DST_CHAIN = 84532; // Base Sepolia
    uint256 internal constant MIN_AMOUNT = 1 ether;

    function setUp() public {
        src = new BridgeGateway(admin);
        dst = new BridgeGateway(admin);

        srcToken = new MockERC20("Bridged USDC", "bUSDC", 18);
        dstToken = new MockERC20("Bridged USDC", "bUSDC", 18);

        src.setRoute(address(srcToken), DST_CHAIN, address(dstToken), MIN_AMOUNT);
        dst.grantRole(dst.RELAYER_ROLE(), relayer);

        srcToken.mint(alice, 1_000 ether);
        // Destination liquidity is provided by the operator — nothing is minted
        // by the bridge itself.
        dstToken.mint(admin, 10_000 ether);
        dstToken.approve(address(dst), type(uint256).max);
        dst.addLiquidity(address(dstToken), 10_000 ether);

        vm.prank(alice);
        srcToken.approve(address(src), type(uint256).max);
    }

    // ─── Happy path ──────────────────────────────────────────────────────────

    function test_DepositEscrowsAndEmitsDeterministicId() public {
        uint256 amount = 10 ether;

        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), amount, DST_CHAIN, bob);

        // The id must be derivable off-chain by the relayer from public data.
        assertEq(transferId, src.computeTransferId(block.chainid, address(src), 1), "transferId not deterministic");

        assertEq(srcToken.balanceOf(address(src)), amount, "tokens not escrowed");
        assertEq(srcToken.balanceOf(alice), 990 ether, "sender not debited");

        (address sender, address token, uint256 stored, uint256 dstChainId, bool exists) = src.deposits(transferId);
        assertTrue(exists);
        assertEq(sender, alice);
        assertEq(token, address(srcToken));
        assertEq(stored, amount);
        assertEq(dstChainId, DST_CHAIN);
    }

    function testFulfillReleasesLiquidityToRecipient() public {
        uint256 amount = 10 ether;
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), amount, DST_CHAIN, bob);

        vm.prank(relayer);
        dst.fulfill(transferId, address(dstToken), bob, amount, block.chainid);

        assertEq(dstToken.balanceOf(bob), amount, "recipient not paid");
        assertTrue(dst.fulfilled(transferId), "transfer not marked fulfilled");
    }

    // ─── Replay protection ───────────────────────────────────────────────────

    function test_FulfillTwiceReverts() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        vm.prank(relayer);
        dst.fulfill(transferId, address(dstToken), bob, 10 ether, block.chainid);

        // A relayer retrying after a dropped receipt must not pay twice.
        vm.prank(relayer);
        vm.expectRevert(abi.encodeWithSelector(BridgeGateway.TransferAlreadyFulfilled.selector, transferId));
        dst.fulfill(transferId, address(dstToken), bob, 10 ether, block.chainid);

        assertEq(dstToken.balanceOf(bob), 10 ether, "recipient paid twice");
    }

    function test_TransferIdsAreUniquePerDeposit() public {
        vm.startPrank(alice);
        bytes32 first = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);
        bytes32 second = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);
        vm.stopPrank();

        // Identical parameters must still produce distinct ids, or the second
        // transfer would be silently swallowed as a replay of the first.
        assertTrue(first != second, "identical deposits collided");
    }

    function test_TransferIdIsUniqueAcrossGateways() public {
        // Same chain id and nonce, different gateway address → different id.
        // Without the gateway in the preimage, a redeployment could replay ids.
        assertTrue(
            src.computeTransferId(block.chainid, address(src), 1)
                != src.computeTransferId(block.chainid, address(dst), 1),
            "id collides across gateways"
        );
        assertTrue(
            src.computeTransferId(1, address(src), 1) != src.computeTransferId(2, address(src), 1),
            "id collides across chains"
        );
    }

    // ─── Route gating ────────────────────────────────────────────────────────

    function test_DepositOnUnmappedRouteReverts() public {
        MockERC20 other = new MockERC20("Other", "OTH", 18);
        other.mint(alice, 100 ether);
        vm.startPrank(alice);
        other.approve(address(src), type(uint256).max);

        // Escrowing on a route the relayer cannot deliver would strand funds.
        vm.expectRevert(abi.encodeWithSelector(BridgeGateway.RouteNotSupported.selector, address(other), DST_CHAIN));
        src.deposit(address(other), 10 ether, DST_CHAIN, bob);
        vm.stopPrank();
    }

    function test_DisablingRouteBlocksNewDeposits() public {
        src.setRoute(address(srcToken), DST_CHAIN, address(0), MIN_AMOUNT);

        vm.prank(alice);
        vm.expectRevert(
            abi.encodeWithSelector(BridgeGateway.RouteNotSupported.selector, address(srcToken), DST_CHAIN)
        );
        src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);
    }

    function test_DepositBelowMinimumReverts() public {
        vm.prank(alice);
        vm.expectRevert(
            abi.encodeWithSelector(BridgeGateway.AmountBelowMinimum.selector, 0.5 ether, MIN_AMOUNT)
        );
        src.deposit(address(srcToken), 0.5 ether, DST_CHAIN, bob);
    }

    function test_SameChainDepositReverts() public {
        vm.prank(alice);
        vm.expectRevert(BridgeGateway.SameChain.selector);
        src.deposit(address(srcToken), 10 ether, block.chainid, bob);
    }

    function test_ZeroRecipientReverts() public {
        vm.prank(alice);
        vm.expectRevert(BridgeGateway.ZeroAddress.selector);
        src.deposit(address(srcToken), 10 ether, DST_CHAIN, address(0));
    }

    // ─── Liquidity honesty ───────────────────────────────────────────────────

    function test_FulfillBeyondLiquidityRevertsRatherThanPartiallyFilling() public {
        BridgeGateway dry = new BridgeGateway(admin);
        dry.grantRole(dry.RELAYER_ROLE(), relayer);
        dstToken.mint(address(dry), 5 ether); // less than the transfer

        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        vm.prank(relayer);
        vm.expectRevert(
            abi.encodeWithSelector(BridgeGateway.InsufficientLiquidity.selector, address(dstToken), 10 ether, 5 ether)
        );
        dry.fulfill(transferId, address(dstToken), bob, 10 ether, block.chainid);

        // A half-delivered transfer has no honest status, so nothing moved and
        // the id stays unburned for a retry once liquidity is topped up.
        assertEq(dstToken.balanceOf(bob), 0, "partial fill leaked");
        assertFalse(dry.fulfilled(transferId), "id burned on a failed fulfill");
    }

    function test_AvailableLiquidityReportsRealBalance() public view {
        assertEq(dst.availableLiquidity(address(dstToken)), 10_000 ether);
    }

    // ─── Access control ──────────────────────────────────────────────────────

    function test_NonRelayerCannotFulfill() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        // Read the role BEFORE pranking: vm.prank applies to the next call, and
        // a view call here would consume it.
        bytes32 relayerRole = dst.RELAYER_ROLE();

        vm.prank(attacker);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector, attacker, relayerRole
            )
        );
        dst.fulfill(transferId, address(dstToken), attacker, 10 ether, block.chainid);
    }

    function test_NonAdminCannotSetRouteOrWithdraw() public {
        vm.startPrank(attacker);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector, attacker, bytes32(0)
            )
        );
        src.setRoute(address(srcToken), DST_CHAIN, address(srcToken), 0);

        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector, attacker, bytes32(0)
            )
        );
        dst.withdrawLiquidity(address(dstToken), attacker, 1 ether);
        vm.stopPrank();
    }

    function test_RevokedRelayerCannotFulfill() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        bytes32 relayerRole = dst.RELAYER_ROLE();
        dst.revokeRole(relayerRole, relayer);

        vm.prank(relayer);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector, relayer, relayerRole
            )
        );
        dst.fulfill(transferId, address(dstToken), bob, 10 ether, block.chainid);
    }

    // ─── Refund ──────────────────────────────────────────────────────────────

    function testRefundReturnsExactEscrowToDepositor() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);
        assertEq(srcToken.balanceOf(alice), 990 ether);

        src.refund(transferId);

        assertEq(srcToken.balanceOf(alice), 1_000 ether, "refund did not restore the depositor");
        assertTrue(src.refunded(transferId));
    }

    function test_RefundTwiceReverts() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);
        src.refund(transferId);

        vm.expectRevert(abi.encodeWithSelector(BridgeGateway.TransferAlreadyRefunded.selector, transferId));
        src.refund(transferId);
    }

    function test_RefundUnknownTransferReverts() public {
        bytes32 ghost = keccak256("never happened");
        vm.expectRevert(abi.encodeWithSelector(BridgeGateway.UnknownTransfer.selector, ghost));
        src.refund(ghost);
    }

    function test_NonAdminCannotRefund() public {
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        vm.prank(attacker);
        vm.expectRevert(
            abi.encodeWithSelector(
                IAccessControl.AccessControlUnauthorizedAccount.selector, attacker, bytes32(0)
            )
        );
        src.refund(transferId);
    }

    // ─── Pause ───────────────────────────────────────────────────────────────

    function test_PauseStopsDepositsAndFulfills() public {
        src.pause();
        vm.prank(alice);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        src.unpause();
        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), 10 ether, DST_CHAIN, bob);

        dst.pause();
        vm.prank(relayer);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        dst.fulfill(transferId, address(dstToken), bob, 10 ether, block.chainid);
    }

    // ─── Fee-on-transfer honesty ─────────────────────────────────────────────

    function test_FeeOnTransferTokenEscrowsMeasuredDelta() public {
        FeeToken fee = new FeeToken();
        fee.mint(alice, 100 ether);
        src.setRoute(address(fee), DST_CHAIN, address(dstToken), MIN_AMOUNT);

        vm.startPrank(alice);
        fee.approve(address(src), type(uint256).max);
        bytes32 transferId = src.deposit(address(fee), 10 ether, DST_CHAIN, bob);
        vm.stopPrank();

        // FeeToken burns 10% on transfer. The recorded amount must be what the
        // gateway ACTUALLY received (9), not what the caller asked for (10) —
        // otherwise the destination would release more than was escrowed.
        (,, uint256 stored,,) = src.deposits(transferId);
        assertEq(stored, 9 ether, "escrow recorded the requested amount, not the received amount");
        assertEq(fee.balanceOf(address(src)), 9 ether);
    }

    // ─── Fuzz ────────────────────────────────────────────────────────────────

    function testFuzz_DepositThenFulfillConservesValue(uint96 raw) public {
        uint256 amount = bound(uint256(raw), MIN_AMOUNT, 1_000 ether);

        vm.prank(alice);
        bytes32 transferId = src.deposit(address(srcToken), amount, DST_CHAIN, bob);

        uint256 liquidityBefore = dstToken.balanceOf(address(dst));
        vm.prank(relayer);
        dst.fulfill(transferId, address(dstToken), bob, amount, block.chainid);

        assertEq(srcToken.balanceOf(address(src)), amount, "escrow mismatch");
        assertEq(dstToken.balanceOf(bob), amount, "delivery mismatch");
        assertEq(dstToken.balanceOf(address(dst)), liquidityBefore - amount, "liquidity accounting drifted");
    }
}

/// Minimal fee-on-transfer token: burns 10% of every transfer.
contract FeeToken is MockERC20 {
    constructor() MockERC20("Fee", "FEE", 18) {}

    function _update(address from, address to, uint256 value) internal override {
        if (from != address(0) && to != address(0)) {
            uint256 fee = value / 10;
            super._update(from, address(0), fee);
            super._update(from, to, value - fee);
            return;
        }
        super._update(from, to, value);
    }
}
