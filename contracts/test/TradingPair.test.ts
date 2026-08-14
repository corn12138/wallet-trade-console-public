import { expect } from "chai";
import { ethers as ethersLib } from "ethers";
import { getHardhatEthers, loadHardhatFixture } from "../hardhat-runtime.js";
import { TradingPair, TradingPairFactory, MockERC20 } from "../typechain-types";
import { HardhatEthersSigner } from "@nomicfoundation/hardhat-ethers/signers";

describe("TradingPair", function () {
  const INITIAL_SUPPLY = ethersLib.parseEther("1000000");

  async function deployFixture() {
    const ethers = await getHardhatEthers();
    const [owner, alice, bob] = await ethers.getSigners();

    // Deploy Factory
    const TradingPairFactory = await ethers.getContractFactory("TradingPairFactory");
    const factory = await TradingPairFactory.deploy(owner.address);

    // Deploy Test Tokens
    const MockERC20 = await ethers.getContractFactory("MockERC20");
    const tokenA = await MockERC20.deploy("Token A", "TKA", 18);
    const tokenB = await MockERC20.deploy("Token B", "TKB", 18);

    // Create Trading Pair
    await factory.createPair(await tokenA.getAddress(), await tokenB.getAddress());
    const pairAddress = await factory.getPair(await tokenA.getAddress(), await tokenB.getAddress());
    const pair = await ethers.getContractAt("TradingPair", pairAddress);

    // Determine token order (token0 is the one with smaller address)
    const tokenAAddress = await tokenA.getAddress();
    const tokenBAddress = await tokenB.getAddress();
    const isTokenAToken0 = tokenAAddress.toLowerCase() < tokenBAddress.toLowerCase();
    const token0 = isTokenAToken0 ? tokenA : tokenB;
    const token1 = isTokenAToken0 ? tokenB : tokenA;

    // Mint tokens to users
    await tokenA.mint(alice.address, INITIAL_SUPPLY);
    await tokenB.mint(alice.address, INITIAL_SUPPLY);
    await tokenA.mint(bob.address, INITIAL_SUPPLY);
    await tokenB.mint(bob.address, INITIAL_SUPPLY);

    return { factory, pair, tokenA, tokenB, token0, token1, owner, alice, bob };
  }

  // Helper function to add liquidity
  async function addLiquidity(
    pair: TradingPair,
    tokenA: MockERC20,
    tokenB: MockERC20,
    provider: HardhatEthersSigner,
    amount0: bigint,
    amount1: bigint
  ) {
    await tokenA.connect(provider).transfer(await pair.getAddress(), amount0);
    await tokenB.connect(provider).transfer(await pair.getAddress(), amount1);
    return await pair.connect(provider).mint(provider.address);
  }

  // Helper function to calculate swap output
  function getAmountOut(amountIn: bigint, reserveIn: bigint, reserveOut: bigint): bigint {
    const amountInWithFee = amountIn * 997n;
    const numerator = amountInWithFee * reserveOut;
    const denominator = reserveIn * 1000n + amountInWithFee;
    return numerator / denominator;
  }

  describe("Add Liquidity", function () {
    it("should add liquidity for the first time", async function () {
      const { pair, tokenA, tokenB, alice } = await loadHardhatFixture(deployFixture);

      const amountA = ethersLib.parseEther("10000");
      const amountB = ethersLib.parseEther("10000");

      await tokenA.connect(alice).transfer(await pair.getAddress(), amountA);
      await tokenB.connect(alice).transfer(await pair.getAddress(), amountB);
      const tx = await pair.connect(alice).mint(alice.address);
      await tx.wait();

      const liquidity = await pair.balanceOf(alice.address);
      expect(liquidity).to.be.gt(0);

      const [reserve0, reserve1] = await pair.getReserves();
      expect(reserve0).to.equal(amountA);
      expect(reserve1).to.equal(amountB);
    });

    it("should add liquidity for the second time", async function () {
      const { pair, tokenA, tokenB, alice, bob } = await loadHardhatFixture(deployFixture);

      // First liquidity
      await addLiquidity(
        pair,
        tokenA,
        tokenB,
        alice,
        ethersLib.parseEther("10000"),
        ethersLib.parseEther("10000")
      );

      // Second liquidity
      const amountA = ethersLib.parseEther("5000");
      const amountB = ethersLib.parseEther("5000");

      await tokenA.connect(bob).transfer(await pair.getAddress(), amountA);
      await tokenB.connect(bob).transfer(await pair.getAddress(), amountB);
      await pair.connect(bob).mint(bob.address);

      const liquidity = await pair.balanceOf(bob.address);
      expect(liquidity).to.be.gt(0);
    });
  });

  describe("Swap", function () {
    it("should swap token0 for token1", async function () {
      const { pair, token0, token1, alice, bob } = await loadHardhatFixture(deployFixture);

      await addLiquidity(
        pair,
        token0,
        token1,
        alice,
        ethersLib.parseEther("10000"),
        ethersLib.parseEther("10000")
      );

      const swapAmount = ethersLib.parseEther("1000");
      const [reserve0, reserve1] = await pair.getReserves();
      const expectedOut = getAmountOut(swapAmount, reserve0, reserve1);

      const bobToken1Before = await token1.balanceOf(bob.address);

      await token0.connect(bob).transfer(await pair.getAddress(), swapAmount);
      await pair.connect(bob).swap(0, expectedOut, bob.address, "0x");

      const bobToken1After = await token1.balanceOf(bob.address);
      expect(bobToken1After - bobToken1Before).to.equal(expectedOut);
    });

    it("should swap token1 for token0", async function () {
      const { pair, token0, token1, alice, bob } = await loadHardhatFixture(deployFixture);

      await addLiquidity(
        pair,
        token0,
        token1,
        alice,
        ethersLib.parseEther("10000"),
        ethersLib.parseEther("10000")
      );

      const swapAmount = ethersLib.parseEther("1000");
      const [reserve0, reserve1] = await pair.getReserves();
      const expectedOut = getAmountOut(swapAmount, reserve1, reserve0);

      const bobToken0Before = await token0.balanceOf(bob.address);

      await token1.connect(bob).transfer(await pair.getAddress(), swapAmount);
      await pair.connect(bob).swap(expectedOut, 0, bob.address, "0x");

      const bobToken0After = await token0.balanceOf(bob.address);
      expect(bobToken0After - bobToken0Before).to.equal(expectedOut);
    });
  });

  describe("Remove Liquidity", function () {
    it("should remove liquidity and return tokens", async function () {
      const { pair, token0, token1, alice } = await loadHardhatFixture(deployFixture);

      await addLiquidity(
        pair, token0, token1, alice,
        ethersLib.parseEther("10000"), ethersLib.parseEther("10000")
      );

      // Get LP token balance after adding liquidity
      const liquidity = await pair.balanceOf(alice.address);

      const aliceToken0Before = await token0.balanceOf(alice.address);
      const aliceToken1Before = await token1.balanceOf(alice.address);

      // Transfer LP tokens to pair and burn
      await pair.connect(alice).transfer(await pair.getAddress(), liquidity);
      await pair.connect(alice).burn(alice.address);

      const aliceToken0After = await token0.balanceOf(alice.address);
      const aliceToken1After = await token1.balanceOf(alice.address);

      expect(aliceToken0After).to.be.gt(aliceToken0Before);
      expect(aliceToken1After).to.be.gt(aliceToken1Before);
    });
  });

  describe("Error Cases", function () {
    it("should revert when insufficient liquidity", async function () {
      const { pair, tokenA, tokenB, alice, bob } = await loadHardhatFixture(deployFixture);

      await addLiquidity(
        pair,
        tokenA,
        tokenB,
        alice,
        ethersLib.parseEther("10000"),
        ethersLib.parseEther("10000")
      );

      await tokenA.connect(bob).transfer(await pair.getAddress(), ethersLib.parseEther("1000"));

      await expect(
        pair.connect(bob).swap(0, ethersLib.parseEther("20000"), bob.address, "0x")
      ).to.be.revertedWith("TradingPair: INSUFFICIENT_LIQUIDITY");
    });

    it("should revert when K value violated", async function () {
      const { pair, tokenA, tokenB, alice, bob } = await loadHardhatFixture(deployFixture);

      await addLiquidity(
        pair,
        tokenA,
        tokenB,
        alice,
        ethersLib.parseEther("10000"),
        ethersLib.parseEther("10000")
      );

      await tokenA.connect(bob).transfer(await pair.getAddress(), ethersLib.parseEther("1000"));

      // Try to get more than allowed (should be ~906 ether, trying 2000)
      await expect(
        pair.connect(bob).swap(0, ethersLib.parseEther("2000"), bob.address, "0x")
      ).to.be.revertedWith("TradingPair: K");
    });
  });
});
