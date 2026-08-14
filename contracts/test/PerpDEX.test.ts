/**
 * Perpetual DEX 合约测试
 *
 * 测试覆盖:
 *   - PerpOracle: 手动价格设置、Chainlink模式、权限控制
 *   - PerpVault: 流动性添加/移除、Market专属函数、储备量管理
 *   - PerpMarket: 开仓/平仓、PnL 计算、清算、费用、杠杆限制
 *   - PositionManager: 用户入口、滑点保护、deadline
 *   - 端到端: 完整交易生命周期
 */

import { expect } from "chai";
import { Wallet, ethers as ethersLib } from "ethers";
import { getHardhatEthers, loadHardhatFixture } from "../hardhat-runtime.js";
import { HardhatEthersSigner } from "@nomicfoundation/hardhat-ethers/signers";

// Constants matching contract
const PRICE_PRECISION = 10n ** 30n;
const BASIS_POINTS_DIVISOR = 10000n;
const MARGIN_FEE_BPS = 10n; // 0.1%
const MIN_LEVERAGE = 10000n; // 1x
const MAX_LEVERAGE = 500000n; // 50x

// Helper: price in 30 decimals
function toPrice(usd: number): bigint {
  return ethersLib.parseUnits(usd.toString(), 30);
}

// Helper: token amount in 18 decimals
function toToken(amount: number): bigint {
  return ethersLib.parseUnits(amount.toString(), 18);
}

// Helper: USD amount in 30 decimals
function toUsd(amount: number): bigint {
  return ethersLib.parseUnits(amount.toString(), 30);
}

async function deployFixture() {
  const ethers = await getHardhatEthers();
  const [owner, user1, user2, feeReceiver, liquidator] = await ethers.getSigners();

  // Deploy test tokens
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const usdc = await MockERC20.deploy("Mock USDC", "USDC", 18);
  const weth = await MockERC20.deploy("Mock WETH", "WETH", 18);

  // Deploy perp contracts
  const PerpOracle = await ethers.getContractFactory("PerpOracle");
  const oracle = await PerpOracle.deploy();

  const PerpVault = await ethers.getContractFactory("PerpVault");
  const vault = await PerpVault.deploy();

  const PerpMarket = await ethers.getContractFactory("PerpMarket");
  const market = await PerpMarket.deploy(
    await vault.getAddress(),
    await oracle.getAddress(),
    feeReceiver.address
  );

  const PositionManager = await ethers.getContractFactory("PositionManager");
  const positionManager = await PositionManager.deploy(
    await market.getAddress(),
    await oracle.getAddress()
  );

  // Wire contracts
  await vault.setMarket(await market.getAddress());
  await market.setPositionManager(await positionManager.getAddress());

  // Set oracle prices
  await oracle.setManualPrice(await weth.getAddress(), toPrice(3450));
  await oracle.setManualPrice(await usdc.getAddress(), toPrice(1));

  // Seed vault liquidity
  const vaultUsdc = toToken(500_000);
  const vaultWeth = toToken(500);
  await usdc.mint(owner.address, vaultUsdc);
  await usdc.approve(await vault.getAddress(), vaultUsdc);
  await vault.addLiquidity(await usdc.getAddress(), vaultUsdc);

  await weth.mint(owner.address, vaultWeth);
  await weth.approve(await vault.getAddress(), vaultWeth);
  await vault.addLiquidity(await weth.getAddress(), vaultWeth);

  // Mint test tokens to users
  await usdc.mint(user1.address, toToken(100_000));
  await usdc.mint(user2.address, toToken(100_000));
  await weth.mint(user1.address, toToken(100));
  await weth.mint(user2.address, toToken(100));

  const deadline = Math.floor(Date.now() / 1000) + 3600;

  return {
    owner, user1, user2, feeReceiver, liquidator,
    usdc, weth, oracle, vault, market, positionManager,
    deadline,
  };
}

describe("PerpOracle", function () {
  it("should return manual price", async function () {
    const { oracle, weth } = await loadHardhatFixture(deployFixture);
    const price = await oracle.getPrice(await weth.getAddress());
    expect(price).to.equal(toPrice(3450));
  });

  it("should revert for unset token price", async function () {
    const { oracle } = await loadHardhatFixture(deployFixture);
    const randomAddr = Wallet.createRandom().address;
    await expect(oracle.getPrice(randomAddr)).to.be.revertedWith("PerpOracle: INVALID_PRICE");
  });

  it("should allow owner to update price", async function () {
    const { oracle, weth } = await loadHardhatFixture(deployFixture);
    await oracle.setManualPrice(await weth.getAddress(), toPrice(4000));
    expect(await oracle.getPrice(await weth.getAddress())).to.equal(toPrice(4000));
  });

  it("should reject non-owner price set", async function () {
    const { oracle, weth, user1 } = await loadHardhatFixture(deployFixture);
    await expect(
      oracle.connect(user1).setManualPrice(await weth.getAddress(), toPrice(999))
    ).to.be.revertedWithCustomError(oracle, "OwnableUnauthorizedAccount");
  });

  it("should emit ManualPriceSet event", async function () {
    const { oracle, weth } = await loadHardhatFixture(deployFixture);
    const wethAddr = await weth.getAddress();
    await expect(oracle.setManualPrice(wethAddr, toPrice(5000)))
      .to.emit(oracle, "ManualPriceSet")
      .withArgs(wethAddr, toPrice(5000));
  });

  it("should toggle manual mode", async function () {
    const { oracle } = await loadHardhatFixture(deployFixture);
    expect(await oracle.isManualMode()).to.be.true;
    await oracle.setManualMode(false);
    expect(await oracle.isManualMode()).to.be.false;
  });
});

describe("PerpVault", function () {
  it("should track pool amounts after addLiquidity", async function () {
    const { vault, usdc } = await loadHardhatFixture(deployFixture);
    const usdcAddr = await usdc.getAddress();
    expect(await vault.poolAmounts(usdcAddr)).to.equal(toToken(500_000));
  });

  it("should allow owner to removeLiquidity", async function () {
    const { vault, usdc, owner } = await loadHardhatFixture(deployFixture);
    const usdcAddr = await usdc.getAddress();
    const before = await usdc.balanceOf(owner.address);
    await vault.removeLiquidity(usdcAddr, toToken(1000), owner.address);
    const after_ = await usdc.balanceOf(owner.address);
    expect(after_ - before).to.equal(toToken(1000));
    expect(await vault.poolAmounts(usdcAddr)).to.equal(toToken(499_000));
  });

  it("should reject non-owner removeLiquidity", async function () {
    const { vault, usdc, user1 } = await loadHardhatFixture(deployFixture);
    await expect(
      vault.connect(user1).removeLiquidity(await usdc.getAddress(), toToken(1), user1.address)
    ).to.be.revertedWithCustomError(vault, "OwnableUnauthorizedAccount");
  });

  it("should reject non-market deposit/withdraw", async function () {
    const { vault, usdc, user1 } = await loadHardhatFixture(deployFixture);
    const usdcAddr = await usdc.getAddress();
    await expect(
      vault.connect(user1).deposit(usdcAddr, toToken(100))
    ).to.be.revertedWith("PerpVault: FORBIDDEN");
    await expect(
      vault.connect(user1).withdraw(usdcAddr, toToken(100), user1.address)
    ).to.be.revertedWith("PerpVault: FORBIDDEN");
  });

  it("should reject removeLiquidity exceeding unreserved amount", async function () {
    const ethers = await getHardhatEthers();
    const { vault, usdc } = await loadHardhatFixture(deployFixture);
    const usdcAddr = await usdc.getAddress();
    await expect(
      vault.removeLiquidity(usdcAddr, toToken(600_000), (await ethers.getSigners())[0].address)
    ).to.be.revertedWith("PerpVault: INSUFFICIENT_POOL");
  });
});

describe("PerpMarket", function () {
  describe("openPosition", function () {
    it("should reject calls from non-PositionManager", async function () {
      const { market, user1, weth, usdc } = await loadHardhatFixture(deployFixture);
      await expect(
        market.connect(user1).openPosition(
          user1.address,
          await weth.getAddress(),
          await usdc.getAddress(),
          toToken(100),
          toUsd(1000),
          true,
          toPrice(3450)
        )
      ).to.be.revertedWith("PerpMarket: FORBIDDEN");
    });
  });

  describe("getPositionKey", function () {
    it("should be deterministic", async function () {
      const { market, user1, weth, usdc } = await loadHardhatFixture(deployFixture);
      const key1 = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      const key2 = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      expect(key1).to.equal(key2);
    });

    it("should differ for long vs short", async function () {
      const { market, user1, weth, usdc } = await loadHardhatFixture(deployFixture);
      const longKey = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      const shortKey = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), false
      );
      expect(longKey).to.not.equal(shortKey);
    });
  });
});

describe("PositionManager", function () {
  describe("openPosition", function () {
    it("should open a long position with 5x leverage", async function () {
      const { positionManager, market, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(100); // 100 USDC
      const sizeDelta = toUsd(500); // $500 = 5x leverage

      // Approve PositionManager to spend USDC
      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          true, // isLong
          0,    // no price limit
          deadline
        )
      ).to.emit(market, "IncreasePosition");

      // Verify position exists
      const key = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      const position = await market.positions(key);
      expect(position.size).to.equal(sizeDelta);
      expect(position.averagePrice).to.equal(toPrice(3450));
    });

    it("should open a short position", async function () {
      const { positionManager, market, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(200);
      const sizeDelta = toUsd(2000); // 10x leverage

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        false, // isShort
        0,
        deadline
      );

      const key = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), false
      );
      const position = await market.positions(key);
      expect(position.size).to.equal(sizeDelta);
    });

    it("should reject leverage below 1x", async function () {
      const { positionManager, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(500); // 0.5x — too low

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          true,
          0,
          deadline
        )
      ).to.be.revertedWith("PerpMarket: LEVERAGE_TOO_LOW");
    });

    it("should reject leverage above 50x", async function () {
      const { positionManager, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(10);
      const sizeDelta = toUsd(600); // 60x — too high

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          true,
          0,
          deadline
        )
      ).to.be.revertedWith("PerpMarket: MAX_LEVERAGE_EXCEEDED");
    });

    it("should reject expired deadline", async function () {
      const { positionManager, usdc, weth, user1 } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(100);
      const sizeDelta = toUsd(500);
      const pastDeadline = 1; // long expired

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          true,
          0,
          pastDeadline
        )
      ).to.be.revertedWith("PositionManager: EXPIRED");
    });

    it("should enforce slippage for long (price too high)", async function () {
      const { positionManager, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(100);
      const sizeDelta = toUsd(500);
      const maxPrice = toPrice(3000); // ETH is $3450, this limit is too low

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          true,
          maxPrice,
          deadline
        )
      ).to.be.revertedWith("PositionManager: PRICE_TOO_HIGH");
    });

    it("should enforce slippage for short (price too low)", async function () {
      const { positionManager, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(100);
      const sizeDelta = toUsd(500);
      const minPrice = toPrice(4000); // ETH is $3450, this limit is too high

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);

      await expect(
        positionManager.connect(user1).openPosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          collateral,
          sizeDelta,
          false,
          minPrice,
          deadline
        )
      ).to.be.revertedWith("PositionManager: PRICE_TOO_LOW");
    });
  });

  describe("closePosition", function () {
    it("should close a profitable long position", async function () {
      const { positionManager, market, oracle, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(5000); // 5x leverage

      // Open long at $3450
      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        true,
        0,
        deadline
      );

      // Price goes up to $3800 (~10% gain)
      await oracle.setManualPrice(await weth.getAddress(), toPrice(3800));

      const balanceBefore = await usdc.balanceOf(user1.address);

      // Close full position
      await positionManager.connect(user1).closePosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        0,
        sizeDelta, // close full size
        true,
        0,
        deadline
      );

      const balanceAfter = await usdc.balanceOf(user1.address);
      // Should receive collateral + profit - fees
      expect(balanceAfter).to.be.gt(balanceBefore);

      // Position should be deleted
      const key = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      const position = await market.positions(key);
      expect(position.size).to.equal(0);
    });

    it("should close a losing long position", async function () {
      const { positionManager, oracle, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(5000); // 5x leverage

      // Open long at $3450
      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        true,
        0,
        deadline
      );

      // Price drops to $3200 (~7.2% loss)
      await oracle.setManualPrice(await weth.getAddress(), toPrice(3200));

      const balanceBefore = await usdc.balanceOf(user1.address);

      await positionManager.connect(user1).closePosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        0,
        sizeDelta,
        true,
        0,
        deadline
      );

      const balanceAfter = await usdc.balanceOf(user1.address);
      // Should receive less than initial collateral
      const received = balanceAfter - balanceBefore;
      expect(received).to.be.lt(collateral);
      expect(received).to.be.gt(0); // Should still get something back
    });

    it("should close a profitable short position", async function () {
      const { positionManager, oracle, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(5000);

      // Open short at $3450
      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        false, // short
        0,
        deadline
      );

      // Price drops to $3000 — profitable for short
      await oracle.setManualPrice(await weth.getAddress(), toPrice(3000));

      const balanceBefore = await usdc.balanceOf(user1.address);
      await positionManager.connect(user1).closePosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        0,
        sizeDelta,
        false,
        0,
        deadline
      );

      const balanceAfter = await usdc.balanceOf(user1.address);
      expect(balanceAfter).to.be.gt(balanceBefore + collateral); // Got more than collateral back
    });

    it("should handle partial close", async function () {
      const { positionManager, market, usdc, weth, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(5000);

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        true,
        0,
        deadline
      );

      // Close half
      await positionManager.connect(user1).closePosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        0,
        toUsd(2500), // half
        true,
        0,
        deadline
      );

      const key = await market.getPositionKey(
        user1.address, await weth.getAddress(), await usdc.getAddress(), true
      );
      const position = await market.positions(key);
      expect(position.size).to.equal(toUsd(2500));
    });

    it("should reject closing non-existent position", async function () {
      const { positionManager, weth, usdc, user1, deadline } =
        await loadHardhatFixture(deployFixture);

      await expect(
        positionManager.connect(user1).closePosition(
          await weth.getAddress(),
          await usdc.getAddress(),
          0,
          toUsd(1000),
          true,
          0,
          deadline
        )
      ).to.be.revertedWith("PerpMarket: NO_POSITION");
    });
  });

  describe("fees", function () {
    it("should collect 0.1% fee on open and close", async function () {
      const { positionManager, usdc, weth, user1, feeReceiver, deadline } =
        await loadHardhatFixture(deployFixture);

      const feeBalanceBefore = await usdc.balanceOf(feeReceiver.address);

      const collateral = toToken(1000);
      const sizeDelta = toUsd(5000);

      await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
      await positionManager.connect(user1).openPosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        collateral,
        sizeDelta,
        true,
        0,
        deadline
      );

      // Close
      await positionManager.connect(user1).closePosition(
        await weth.getAddress(),
        await usdc.getAddress(),
        0,
        sizeDelta,
        true,
        0,
        deadline
      );

      const feeBalanceAfter = await usdc.balanceOf(feeReceiver.address);
      // Fee = 0.1% * $5000 = $5 per trade, total $10 for open + close
      // In token terms: $10 at $1/USDC = 10 USDC
      expect(feeBalanceAfter - feeBalanceBefore).to.equal(toToken(10));
    });
  });
});

describe("Liquidation", function () {
  it("should liquidate an underwater position", async function () {
    const { positionManager, market, oracle, usdc, weth, user1, liquidator, deadline } =
      await loadHardhatFixture(deployFixture);

    // Open 10x long at $3450 with $200 collateral → $2000 size
    const collateral = toToken(200);
    const sizeDelta = toUsd(2000);

    await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
    await positionManager.connect(user1).openPosition(
      await weth.getAddress(),
      await usdc.getAddress(),
      collateral,
      sizeDelta,
      true,
      0,
      deadline
    );

    // Price drops from $3450 to $3120 (~9.6% drop → ~96% loss on 10x)
    // PnL = (3120-3450)/3450 * 2000 ≈ -$191.3
    // Collateral after fee ≈ $200 - $0.2 = $199.8
    // Remaining = $199.8 - $191.3 ≈ $8.5 < maintenance margin ($20 = 1% of $2000)
    // So liquidatable, but remaining > 0 so liquidator gets fee
    await oracle.setManualPrice(await weth.getAddress(), toPrice(3120));

    const liquidatorBalBefore = await usdc.balanceOf(liquidator.address);

    await expect(
      market.connect(liquidator).liquidatePosition(
        user1.address,
        await weth.getAddress(),
        await usdc.getAddress(),
        true,
        liquidator.address
      )
    ).to.emit(market, "LiquidatePosition");

    // Liquidator should receive liquidation fee (up to $5 or remaining collateral)
    const liquidatorBalAfter = await usdc.balanceOf(liquidator.address);
    expect(liquidatorBalAfter).to.be.gt(liquidatorBalBefore);

    // Position should be deleted
    const key = await market.getPositionKey(
      user1.address, await weth.getAddress(), await usdc.getAddress(), true
    );
    const position = await market.positions(key);
    expect(position.size).to.equal(0);
  });

  it("should reject liquidation of healthy position", async function () {
    const { positionManager, market, usdc, weth, user1, liquidator, deadline } =
      await loadHardhatFixture(deployFixture);

    // Open 2x long — very safe
    const collateral = toToken(1000);
    const sizeDelta = toUsd(2000);

    await usdc.connect(user1).approve(await positionManager.getAddress(), collateral);
    await positionManager.connect(user1).openPosition(
      await weth.getAddress(),
      await usdc.getAddress(),
      collateral,
      sizeDelta,
      true,
      0,
      deadline
    );

    await expect(
      market.connect(liquidator).liquidatePosition(
        user1.address,
        await weth.getAddress(),
        await usdc.getAddress(),
        true,
        liquidator.address
      )
    ).to.be.revertedWith("PerpMarket: NOT_LIQUIDATABLE");
  });
});

describe("End-to-End: Full Trading Lifecycle", function () {
  it("should handle open → price move → partial close → full close", async function () {
    const { positionManager, market, oracle, usdc, weth, user1, deadline } =
      await loadHardhatFixture(deployFixture);

    const wethAddr = await weth.getAddress();
    const usdcAddr = await usdc.getAddress();
    const pmAddr = await positionManager.getAddress();

    // 1. Open long at $3450, $1000 collateral, 10x = $10000 size
    const collateral = toToken(1000);
    const sizeDelta = toUsd(10000);
    await usdc.connect(user1).approve(pmAddr, collateral);
    await positionManager.connect(user1).openPosition(
      wethAddr, usdcAddr, collateral, sizeDelta, true, 0, deadline
    );

    // 2. Price goes to $3600 (~4.3% gain)
    await oracle.setManualPrice(wethAddr, toPrice(3600));

    // 3. Partial close: close $5000 (half)
    const bal1 = await usdc.balanceOf(user1.address);
    await positionManager.connect(user1).closePosition(
      wethAddr, usdcAddr, 0, toUsd(5000), true, 0, deadline
    );
    const bal2 = await usdc.balanceOf(user1.address);
    const partialPayout = bal2 - bal1;
    expect(partialPayout).to.be.gt(0);

    // 4. Verify remaining position is half
    const key = await market.getPositionKey(user1.address, wethAddr, usdcAddr, true);
    const pos = await market.positions(key);
    expect(pos.size).to.equal(toUsd(5000));

    // 5. Price drops back to $3450
    await oracle.setManualPrice(wethAddr, toPrice(3450));

    // 6. Close remaining position (PnL should be ~0 since back to entry)
    await positionManager.connect(user1).closePosition(
      wethAddr, usdcAddr, 0, toUsd(5000), true, 0, deadline
    );

    // Position fully closed
    const finalPos = await market.positions(key);
    expect(finalPos.size).to.equal(0);
  });

  it("should handle multiple users trading simultaneously", async function () {
    const { positionManager, market, oracle, usdc, weth, user1, user2, deadline } =
      await loadHardhatFixture(deployFixture);

    const wethAddr = await weth.getAddress();
    const usdcAddr = await usdc.getAddress();
    const pmAddr = await positionManager.getAddress();

    // User1 goes long, User2 goes short
    await usdc.connect(user1).approve(pmAddr, toToken(500));
    await usdc.connect(user2).approve(pmAddr, toToken(500));

    await positionManager.connect(user1).openPosition(
      wethAddr, usdcAddr, toToken(500), toUsd(2500), true, 0, deadline
    );
    await positionManager.connect(user2).openPosition(
      wethAddr, usdcAddr, toToken(500), toUsd(2500), false, 0, deadline
    );

    // Verify both positions exist
    const longKey = await market.getPositionKey(user1.address, wethAddr, usdcAddr, true);
    const shortKey = await market.getPositionKey(user2.address, wethAddr, usdcAddr, false);
    expect((await market.positions(longKey)).size).to.equal(toUsd(2500));
    expect((await market.positions(shortKey)).size).to.equal(toUsd(2500));

    // Track global OI
    expect(await market.globalLongSize()).to.equal(toUsd(2500));
    expect(await market.globalShortSize()).to.equal(toUsd(2500));

    // Price goes up — user1 profits, user2 loses
    await oracle.setManualPrice(wethAddr, toPrice(3700));

    const u1Before = await usdc.balanceOf(user1.address);
    const u2Before = await usdc.balanceOf(user2.address);

    await positionManager.connect(user1).closePosition(
      wethAddr, usdcAddr, 0, toUsd(2500), true, 0, deadline
    );
    await positionManager.connect(user2).closePosition(
      wethAddr, usdcAddr, 0, toUsd(2500), false, 0, deadline
    );

    const u1After = await usdc.balanceOf(user1.address);
    const u2After = await usdc.balanceOf(user2.address);

    // User1 (long) should profit, User2 (short) should lose
    expect(u1After - u1Before).to.be.gt(toToken(500)); // Got more than collateral
    expect(u2After - u2Before).to.be.lt(toToken(500)); // Got less than collateral
  });

  it("should track vault pool amounts correctly through trades", async function () {
    const { positionManager, vault, oracle, usdc, weth, user1, deadline } =
      await loadHardhatFixture(deployFixture);

    const wethAddr = await weth.getAddress();
    const usdcAddr = await usdc.getAddress();
    const pmAddr = await positionManager.getAddress();

    const poolBefore = await vault.poolAmounts(usdcAddr);

    // Open and close at same price — vault should only gain fees
    const collateral = toToken(1000);
    const sizeDelta = toUsd(5000);
    await usdc.connect(user1).approve(pmAddr, collateral);
    await positionManager.connect(user1).openPosition(
      wethAddr, usdcAddr, collateral, sizeDelta, true, 0, deadline
    );
    await positionManager.connect(user1).closePosition(
      wethAddr, usdcAddr, 0, sizeDelta, true, 0, deadline
    );

    const poolAfter = await vault.poolAmounts(usdcAddr);
    // Pool should be roughly same (fees go to feeReceiver, not pool)
    // But the net effect of deposit/withdraw should be close to 0
    // Pool changes: +collateral (open) -payout (close) -fees (open+close)
    // At same price, payout = collateral - close_fee, so net = +open_fee
    expect(poolAfter).to.be.gte(poolBefore);
  });
});
