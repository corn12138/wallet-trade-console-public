// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import { Test } from "forge-std/Test.sol";
import { StdInvariant } from "forge-std/StdInvariant.sol";
import { Vm } from "forge-std/Vm.sol";
import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import { BondingCurve } from "../src/launchpad/BondingCurve.sol";
import { LaunchToken } from "../src/launchpad/LaunchToken.sol";
import { TokenFactory } from "../src/launchpad/TokenFactory.sol";

contract GraduationPairLookup {
    address internal constant PAIR = address(0xBEEF);

    function getPair(address, address) external pure returns (address) {
        return PAIR;
    }
}

contract GraduationRouter {
    using SafeERC20 for IERC20;

    uint256 internal constant BPS_DENOMINATOR = 10_000;
    address internal constant WRAPPED_NATIVE = address(0xCAFE);

    GraduationPairLookup internal immutable PAIR_LOOKUP;
    uint256 internal immutable ETH_USAGE_BPS;

    constructor(uint256 ethUsageBps) {
        require(ethUsageBps >= 9500 && ethUsageBps <= BPS_DENOMINATOR, "Invalid usage");
        PAIR_LOOKUP = new GraduationPairLookup();
        ETH_USAGE_BPS = ethUsageBps;
    }

    function WETH() external pure returns (address) {
        return WRAPPED_NATIVE;
    }

    function factory() external view returns (address) {
        return address(PAIR_LOOKUP);
    }

    function addLiquidityETH(
        address token,
        uint256 amountTokenDesired,
        uint256 amountTokenMin,
        uint256 amountEthMin,
        address,
        uint256
    )
        external
        payable
        returns (uint256 amountToken, uint256 amountEth, uint256 liquidity)
    {
        amountToken = (amountTokenDesired * ETH_USAGE_BPS) / BPS_DENOMINATOR;
        amountEth = (msg.value * ETH_USAGE_BPS) / BPS_DENOMINATOR;
        require(amountToken >= amountTokenMin && amountEth >= amountEthMin, "Slippage");

        IERC20(token).safeTransferFrom(msg.sender, address(this), amountToken);

        uint256 refund = msg.value - amountEth;
        if (refund > 0) {
            (bool refunded,) = msg.sender.call{ value: refund }("");
            require(refunded, "Refund failed");
        }

        liquidity = amountToken + amountEth;
    }
}

abstract contract BondingCurveAccountingFixture is Test {
    uint256 internal constant TRADE_FEE_BPS = 300;
    uint256 internal constant LIQUIDITY_BPS = 8000;
    uint256 internal constant CREATOR_BPS = 500;
    uint256 internal constant BPS_DENOMINATOR = 10_000;

    address internal creator;
    address internal feeRecipient;
    address internal buyer;

    function setUp() public virtual {
        creator = makeAddr("creator");
        feeRecipient = makeAddr("feeRecipient");
        buyer = makeAddr("buyer");
    }

    function _deployAndGraduate(
        uint256 ethUsageBps,
        uint256 buyAmount
    )
        internal
        returns (BondingCurve curve)
    {
        GraduationRouter router = new GraduationRouter(ethUsageBps);
        LaunchToken token = new LaunchToken(
            "Accounting Asset", "ACCT", "Graduation accounting fixture", "", "", address(this)
        );
        curve = new BondingCurve(
            address(token),
            1 ether,
            0,
            5000,
            TRADE_FEE_BPS,
            feeRecipient,
            1,
            address(router),
            creator
        );
        token.setBondingCurve(address(curve));
        token.launch(0);

        vm.deal(buyer, buyAmount);
        vm.prank(buyer);
        curve.buy{ value: buyAmount }(0);
        assertTrue(curve.graduated());
    }

    function _expectedAllocations(
        uint256 buyAmount,
        uint256 ethUsageBps
    )
        internal
        pure
        returns (uint256 creatorDue, uint256 protocolDue, uint256 totalDue)
    {
        uint256 tradeFee = (buyAmount * TRADE_FEE_BPS) / BPS_DENOMINATOR;
        uint256 curveReserve = buyAmount - tradeFee;
        uint256 liquidityBudget = (curveReserve * LIQUIDITY_BPS) / BPS_DENOMINATOR;
        uint256 ethSpent = (liquidityBudget * ethUsageBps) / BPS_DENOMINATOR;
        creatorDue = (curveReserve * CREATOR_BPS) / BPS_DENOMINATOR;
        protocolDue = tradeFee + curveReserve - ethSpent - creatorDue;
        totalDue = creatorDue + protocolDue;
    }
}

contract BondingCurveAccountingTest is BondingCurveAccountingFixture {
    function test_GraduationAllocatesOnlyUnencumberedReserve() public {
        uint256 buyAmount = 100 ether;
        uint256 ethUsageBps = 9750;
        BondingCurve curve = _deployAndGraduate(ethUsageBps, buyAmount);

        (uint256 creatorDue, uint256 protocolDue, uint256 totalDue) =
            _expectedAllocations(buyAmount, ethUsageBps);

        assertEq(curve.reserveBalance(), 0);
        assertEq(curve.pendingPayments(creator), creatorDue);
        assertEq(curve.pendingPayments(feeRecipient), protocolDue);
        assertEq(curve.totalPendingPayments(), totalDue);
        assertEq(address(curve).balance, totalDue);

        LaunchToken token = curve.token();
        assertEq(token.balanceOf(address(curve)), 0);
        assertEq(token.allowance(address(curve), address(curve.dexRouter())), 0);
    }

    function test_ClaimsRemainSolventWhenCreatorClaimsFirst() public {
        _assertClaimOrder(true);
    }

    function test_ClaimsRemainSolventWhenProtocolClaimsFirst() public {
        _assertClaimOrder(false);
    }

    function test_EmergencyWithdrawalCannotConsumeAccountedFunds() public {
        BondingCurve curve = _deployAndGraduate(10_000, 100 ether);
        uint256 liabilities = curve.totalPendingPayments();
        address surplusRecipient = makeAddr("surplusRecipient");
        curve.pause();

        vm.expectRevert("No excess ETH to withdraw");
        curve.emergencyWithdraw(surplusRecipient);

        vm.deal(address(curve), liabilities + 1 ether);
        curve.emergencyWithdraw(surplusRecipient);

        assertEq(surplusRecipient.balance, 1 ether);
        assertEq(address(curve).balance, liabilities);
        assertEq(curve.totalPendingPayments(), liabilities);
    }

    function testFuzz_GraduationLiabilitiesNeverExceedBalance(
        uint96 rawBuyAmount,
        uint16 rawEthUsageBps
    )
        public
    {
        uint256 buyAmount = bound(uint256(rawBuyAmount), 1 ether, 100 ether);
        uint256 ethUsageBps = bound(uint256(rawEthUsageBps), 9500, 10_000);
        BondingCurve curve = _deployAndGraduate(ethUsageBps, buyAmount);

        assertLe(curve.totalPendingPayments(), address(curve).balance);
        assertEq(curve.totalPendingPayments(), address(curve).balance);
    }

    function _assertClaimOrder(bool creatorFirst) internal {
        BondingCurve curve = _deployAndGraduate(9750, 100 ether);
        address first = creatorFirst ? creator : feeRecipient;
        address second = creatorFirst ? feeRecipient : creator;
        uint256 firstDue = curve.pendingPayments(first);
        uint256 secondDue = curve.pendingPayments(second);

        vm.prank(first);
        curve.claimPayment();
        assertEq(first.balance, firstDue);
        assertEq(curve.totalPendingPayments(), secondDue);
        assertEq(address(curve).balance, secondDue);

        vm.prank(second);
        curve.claimPayment();
        assertEq(second.balance, secondDue);
        assertEq(curve.totalPendingPayments(), 0);
        assertEq(address(curve).balance, 0);
    }
}

contract TokenFactoryInitialBuyTest is Test {
    uint256 internal constant CREATION_FEE = 0.001 ether;
    bytes32 internal constant BUY_EVENT_SIGNATURE =
        keccak256("Buy(address,uint256,uint256,uint256)");

    address internal creator;
    address internal feeRecipient;
    TokenFactory internal tokenFactory;

    function setUp() public {
        creator = makeAddr("factoryCreator");
        feeRecipient = makeAddr("factoryFeeRecipient");
        GraduationRouter router = new GraduationRouter(10_000);
        tokenFactory = new TokenFactory(CREATION_FEE, feeRecipient, address(router));
        tokenFactory.setDefaultGraduationThreshold(type(uint256).max);
    }

    function test_CreateAndLaunchAttributesInitialBuyToCreator() public {
        uint256 buyAmount = 1 ether;
        vm.deal(creator, CREATION_FEE + buyAmount);
        vm.recordLogs();

        vm.prank(creator);
        (address token, address curve) = tokenFactory.createAndLaunch{
            value: CREATION_FEE + buyAmount
        }(
            "Creator Asset", "CRT", "", "", "", 0
        );

        assertGt(LaunchToken(token).balanceOf(creator), 0);
        assertEq(LaunchToken(token).balanceOf(address(tokenFactory)), 0);

        Vm.Log[] memory entries = vm.getRecordedLogs();
        bool foundCreatorBuy;
        for (uint256 i = 0; i < entries.length; i++) {
            if (entries[i].emitter == curve && entries[i].topics[0] == BUY_EVENT_SIGNATURE) {
                foundCreatorBuy = address(uint160(uint256(entries[i].topics[1]))) == creator;
                break;
            }
        }
        assertTrue(foundCreatorBuy);
    }

    function test_BuyForRejectsArbitraryDelegatedBuyer() public {
        vm.deal(creator, CREATION_FEE);
        vm.prank(creator);
        (, address curve) = tokenFactory.createAndLaunch{ value: CREATION_FEE }(
            "Restricted Asset", "RST", "", "", "", 0
        );

        address arbitraryPayer = makeAddr("arbitraryPayer");
        vm.deal(arbitraryPayer, 1 ether);
        vm.prank(arbitraryPayer);
        vm.expectRevert(
            abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, arbitraryPayer)
        );
        BondingCurve(payable(curve)).buyFor{ value: 1 ether }(creator, 0);
    }

    function testFuzz_InitialBuyNeverCreditsFactory(
        uint96 rawBuyAmount,
        uint96 rawInitialMint
    )
        public
    {
        uint256 buyAmount = bound(uint256(rawBuyAmount), 0.001 ether, 10 ether);
        uint256 initialMint = bound(uint256(rawInitialMint), 0, 1_000_000 ether);
        vm.deal(creator, CREATION_FEE + buyAmount);

        vm.prank(creator);
        (address token,) = tokenFactory.createAndLaunch{ value: CREATION_FEE + buyAmount }(
            "Fuzz Asset", "FZZ", "", "", "", initialMint
        );

        assertGt(LaunchToken(token).balanceOf(creator), 0);
        assertEq(LaunchToken(token).balanceOf(address(tokenFactory)), 0);
    }
}

contract BondingCurveAccountingHandler is Test {
    BondingCurve internal immutable CURVE;
    LaunchToken internal immutable TOKEN;
    address internal immutable CREATOR;
    address internal immutable FEE_RECIPIENT;
    address[] internal actors;

    constructor(BondingCurve curve, LaunchToken token, address creator, address feeRecipient) {
        CURVE = curve;
        TOKEN = token;
        CREATOR = creator;
        FEE_RECIPIENT = feeRecipient;
        actors.push(address(0xA11CE));
        actors.push(address(0xB0B));
        actors.push(address(0xCA11));
    }

    function buy(uint96 rawAmount, uint8 actorSeed) external {
        if (CURVE.graduated()) return;

        address actor = actors[uint256(actorSeed) % actors.length];
        uint256 amount = bound(uint256(rawAmount), 0.01 ether, 2 ether);
        vm.deal(actor, actor.balance + amount);
        vm.roll(block.number + 1);
        vm.prank(actor);
        CURVE.buy{ value: amount }(0);
    }

    function sell(uint16 rawShareBps, uint8 actorSeed) external {
        if (CURVE.graduated()) return;

        address actor = actors[uint256(actorSeed) % actors.length];
        uint256 balance = TOKEN.balanceOf(actor);
        if (balance == 0) return;

        uint256 shareBps = bound(uint256(rawShareBps), 1, 10_000);
        uint256 amount = (balance * shareBps) / 10_000;
        if (amount == 0) amount = balance;

        vm.roll(block.number + 1);
        vm.prank(actor);
        CURVE.sell(amount, 0);
    }

    function claim(uint8 recipientSeed) external {
        address recipient = recipientSeed % 2 == 0 ? CREATOR : FEE_RECIPIENT;
        if (CURVE.pendingPayments(recipient) == 0) return;

        vm.prank(recipient);
        CURVE.claimPayment();
    }

    function addSurplus(uint96 rawAmount) external {
        uint256 amount = bound(uint256(rawAmount), 1 wei, 1 ether);
        vm.deal(address(CURVE), address(CURVE).balance + amount);
    }
}

contract BondingCurveAccountingInvariantTest is StdInvariant, BondingCurveAccountingFixture {
    BondingCurve internal curve;
    BondingCurveAccountingHandler internal handler;

    function setUp() public override {
        super.setUp();

        GraduationRouter router = new GraduationRouter(9750);
        LaunchToken token = new LaunchToken(
            "Invariant Asset", "INV", "Accounting state machine", "", "", address(this)
        );
        curve = new BondingCurve(
            address(token),
            1 ether,
            0,
            5000,
            TRADE_FEE_BPS,
            feeRecipient,
            5 ether,
            address(router),
            creator
        );
        token.setBondingCurve(address(curve));
        token.launch(0);
        curve.setMaxTradesPerBlock(0);

        handler = new BondingCurveAccountingHandler(curve, token, creator, feeRecipient);
        bytes4[] memory selectors = new bytes4[](4);
        selectors[0] = handler.buy.selector;
        selectors[1] = handler.sell.selector;
        selectors[2] = handler.claim.selector;
        selectors[3] = handler.addSurplus.selector;
        targetContract(address(handler));
        targetSelector(FuzzSelector({ addr: address(handler), selectors: selectors }));
    }

    function invariant_ReserveAndPendingLiabilitiesRemainBacked() public view {
        assertLe(curve.reserveBalance() + curve.totalPendingPayments(), address(curve).balance);
    }
}
