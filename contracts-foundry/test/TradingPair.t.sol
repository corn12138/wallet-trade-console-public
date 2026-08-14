// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test, console} from "forge-std/Test.sol";
import {TradingPair} from "../src/core/TradingPair.sol";
import {TradingPairFactory} from "../src/core/TradingPairFactory.sol";
import {MockERC20} from "../src/tokens/MockERC20.sol";

contract TradingPairTest is Test {
    TradingPairFactory public factory;
    TradingPair public pair;
    MockERC20 public token0;
    MockERC20 public token1;

    address public owner = address(this);
    address public alice = makeAddr("alice");
    address public bob = makeAddr("bob");

    uint256 public constant INITIAL_SUPPLY = 1_000_000 ether;

    function setUp() public {
        // Deploy Factory
        factory = new TradingPairFactory(owner);

        // Deploy Test Tokens
        MockERC20 tokenA = new MockERC20("Token A", "TKA", 18);
        MockERC20 tokenB = new MockERC20("Token B", "TKB", 18);

        // Determine token order (token0 has smaller address)
        if (address(tokenA) < address(tokenB)) {
            token0 = tokenA;
            token1 = tokenB;
        } else {
            token0 = tokenB;
            token1 = tokenA;
        }

        // Create Trading Pair
        factory.createPair(address(token0), address(token1));
        address pairAddress = factory.getPair(address(token0), address(token1));
        pair = TradingPair(pairAddress);

        // Mint tokens to users
        token0.mint(alice, INITIAL_SUPPLY);
        token1.mint(alice, INITIAL_SUPPLY);
        token0.mint(bob, INITIAL_SUPPLY);
        token1.mint(bob, INITIAL_SUPPLY);
    }

    // ========== Helper Functions ==========

    function addLiquidity(
        address provider,
        uint256 amount0,
        uint256 amount1
    ) internal returns (uint256 liquidity) {
        vm.startPrank(provider);
        token0.transfer(address(pair), amount0);
        token1.transfer(address(pair), amount1);
        liquidity = pair.mint(provider);
        vm.stopPrank();
    }

    function getAmountOut(
        uint256 amountIn,
        uint256 reserveIn,
        uint256 reserveOut
    ) internal pure returns (uint256) {
        uint256 amountInWithFee = amountIn * 997;
        uint256 numerator = amountInWithFee * reserveOut;
        uint256 denominator = reserveIn * 1000 + amountInWithFee;
        return numerator / denominator;
    }

    // ========== Add Liquidity Tests ==========

    function test_AddLiquidityFirstTime() public {
        uint256 amount0 = 10_000 ether;
        uint256 amount1 = 10_000 ether;

        vm.startPrank(alice);
        token0.transfer(address(pair), amount0);
        token1.transfer(address(pair), amount1);
        pair.mint(alice);
        vm.stopPrank();

        uint256 liquidity = pair.balanceOf(alice);
        assertGt(liquidity, 0, "Should have liquidity tokens");

        (uint112 reserve0, uint112 reserve1, ) = pair.getReserves();
        assertEq(reserve0, amount0, "Reserve0 should match");
        assertEq(reserve1, amount1, "Reserve1 should match");
    }

    function test_AddLiquiditySecondTime() public {
        // First liquidity
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        // Second liquidity
        uint256 bobLiquidityBefore = pair.balanceOf(bob);
        addLiquidity(bob, 5_000 ether, 5_000 ether);
        uint256 bobLiquidityAfter = pair.balanceOf(bob);

        assertGt(bobLiquidityAfter - bobLiquidityBefore, 0, "Bob should have LP tokens");
    }

    // ========== Swap Tests ==========

    function test_SwapToken0ForToken1() public {
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        uint256 swapAmount = 1_000 ether;
        (uint112 reserve0, uint112 reserve1, ) = pair.getReserves();
        uint256 expectedOut = getAmountOut(swapAmount, reserve0, reserve1);

        uint256 bobToken1Before = token1.balanceOf(bob);

        vm.startPrank(bob);
        token0.transfer(address(pair), swapAmount);
        pair.swap(0, expectedOut, bob, "");
        vm.stopPrank();

        uint256 bobToken1After = token1.balanceOf(bob);
        assertEq(bobToken1After - bobToken1Before, expectedOut, "Should receive expected amount");
    }

    function test_SwapToken1ForToken0() public {
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        uint256 swapAmount = 1_000 ether;
        (uint112 reserve0, uint112 reserve1, ) = pair.getReserves();
        uint256 expectedOut = getAmountOut(swapAmount, reserve1, reserve0);

        uint256 bobToken0Before = token0.balanceOf(bob);

        vm.startPrank(bob);
        token1.transfer(address(pair), swapAmount);
        pair.swap(expectedOut, 0, bob, "");
        vm.stopPrank();

        uint256 bobToken0After = token0.balanceOf(bob);
        assertEq(bobToken0After - bobToken0Before, expectedOut, "Should receive expected amount");
    }

    // ========== Remove Liquidity Tests ==========

    function test_RemoveLiquidity() public {
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        uint256 liquidity = pair.balanceOf(alice);
        uint256 aliceToken0Before = token0.balanceOf(alice);
        uint256 aliceToken1Before = token1.balanceOf(alice);

        vm.startPrank(alice);
        pair.transfer(address(pair), liquidity);
        pair.burn(alice);
        vm.stopPrank();

        uint256 aliceToken0After = token0.balanceOf(alice);
        uint256 aliceToken1After = token1.balanceOf(alice);

        assertGt(aliceToken0After, aliceToken0Before, "Should receive token0 back");
        assertGt(aliceToken1After, aliceToken1Before, "Should receive token1 back");
    }

    // ========== Error Cases ==========

    function test_RevertWhen_InsufficientLiquidity() public {
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        vm.startPrank(bob);
        token0.transfer(address(pair), 1_000 ether);

        vm.expectRevert("TradingPair: INSUFFICIENT_LIQUIDITY");
        pair.swap(0, 20_000 ether, bob, "");
        vm.stopPrank();
    }

    function test_RevertWhen_KValueViolated() public {
        addLiquidity(alice, 10_000 ether, 10_000 ether);

        vm.startPrank(bob);
        token0.transfer(address(pair), 1_000 ether);

        // Try to get more than allowed (should be ~906 ether, trying 2000)
        vm.expectRevert("TradingPair: K");
        pair.swap(0, 2_000 ether, bob, "");
        vm.stopPrank();
    }

    // ========== Fuzz Tests (Foundry特有) ==========

    function testFuzz_AddLiquidity(uint256 amount0, uint256 amount1) public {
        // Bound the inputs to reasonable values
        // Minimum must be > sqrt(MINIMUM_LIQUIDITY^2) = 1000 for first mint to succeed
        amount0 = bound(amount0, 1 ether, 100_000 ether);
        amount1 = bound(amount1, 1 ether, 100_000 ether);

        vm.startPrank(alice);
        token0.transfer(address(pair), amount0);
        token1.transfer(address(pair), amount1);
        uint256 liquidity = pair.mint(alice);
        vm.stopPrank();

        assertGt(liquidity, 0, "Should mint liquidity tokens");
    }

    function testFuzz_Swap(uint256 swapAmount) public {
        addLiquidity(alice, 100_000 ether, 100_000 ether);

        // Bound swap amount to reasonable range
        swapAmount = bound(swapAmount, 1 ether, 10_000 ether);

        (uint112 reserve0, uint112 reserve1, ) = pair.getReserves();
        uint256 expectedOut = getAmountOut(swapAmount, reserve0, reserve1);

        vm.startPrank(bob);
        token0.transfer(address(pair), swapAmount);
        pair.swap(0, expectedOut, bob, "");
        vm.stopPrank();

        // Verify K is maintained
        (uint112 newReserve0, uint112 newReserve1, ) = pair.getReserves();
        assertGe(
            uint256(newReserve0) * uint256(newReserve1),
            uint256(reserve0) * uint256(reserve1),
            "K should not decrease"
        );
    }
}
