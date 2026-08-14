/**
 * 本地永续交易合约部署脚本
 *
 * 部署合约:
 *   1. MockUSDC (18 decimals) - 测试抵押品
 *   2. MockWETH (18 decimals) - ETH 指数代币
 *   3. MockWBTC (8 decimals) - BTC 指数代币
 *   4. PerpOracle - 价格预言机 (手动模式)
 *   5. PerpVault - 资金池
 *   6. PerpMarket - 交易引擎
 *   7. PositionManager - 用户入口
 *
 * 使用方法:
 *   Terminal 1: npx hardhat node
 *   Terminal 2: npx hardhat run scripts/deploy-perp-local.ts --network localhost
 */

import * as fs from "fs";
import * as path from "path";
import { getHardhatEthers } from "../hardhat-runtime.js";
import { getScriptDir } from "./runtime-paths.js";

const SCRIPT_DIR = getScriptDir(import.meta.url);

async function main() {
    const ethers = await getHardhatEthers();
    const [deployer] = await ethers.getSigners();

    console.log("╔══════════════════════════════════════════╗");
    console.log("║   Local Perp DEX Deploy + Seed Script    ║");
    console.log("╚══════════════════════════════════════════╝\n");

    console.log("Deployer:", deployer.address);
    const balance = await ethers.provider.getBalance(deployer.address);
    console.log("Balance:", ethers.formatEther(balance), "ETH\n");

    // ========== 1. Deploy Test Tokens ==========
    console.log("━━━ Step 1: Deploying Test Tokens ━━━");

    const MockERC20 = await ethers.getContractFactory("MockERC20");

    // MockUSDC - 18 decimals (PerpMarket._tokenToUsd assumes 18 decimals)
    const mockUsdc = await MockERC20.deploy("Mock USDC", "USDC", 18);
    await mockUsdc.waitForDeployment();
    const usdcAddress = await mockUsdc.getAddress();
    console.log("✅ MockUSDC (18 decimals):", usdcAddress);

    // MockWETH - 18 decimals
    const mockWeth = await MockERC20.deploy("Mock WETH", "WETH", 18);
    await mockWeth.waitForDeployment();
    const wethAddress = await mockWeth.getAddress();
    console.log("✅ MockWETH (18 decimals):", wethAddress);

    // MockWBTC - 8 decimals
    const mockWbtc = await MockERC20.deploy("Mock WBTC", "WBTC", 8);
    await mockWbtc.waitForDeployment();
    const wbtcAddress = await mockWbtc.getAddress();
    console.log("✅ MockWBTC (8 decimals):", wbtcAddress);

    // ========== 2. Deploy Perp Contracts ==========
    console.log("\n━━━ Step 2: Deploying Perp Contracts ━━━");

    // Oracle
    const PerpOracle = await ethers.getContractFactory("PerpOracle");
    const oracle = await PerpOracle.deploy();
    await oracle.waitForDeployment();
    const oracleAddress = await oracle.getAddress();
    console.log("✅ PerpOracle:", oracleAddress);

    // Vault
    const PerpVault = await ethers.getContractFactory("PerpVault");
    const vault = await PerpVault.deploy();
    await vault.waitForDeployment();
    const vaultAddress = await vault.getAddress();
    console.log("✅ PerpVault:", vaultAddress);

    // Market (constructor: vault, oracle)
    const PerpMarket = await ethers.getContractFactory("PerpMarket");
    const market = await PerpMarket.deploy(vaultAddress, oracleAddress, deployer.address);
    await market.waitForDeployment();
    const marketAddress = await market.getAddress();
    console.log("✅ PerpMarket:", marketAddress);

    // PositionManager (constructor: market, oracle)
    const PositionManager = await ethers.getContractFactory("PositionManager");
    const positionManager = await PositionManager.deploy(marketAddress, oracleAddress);
    await positionManager.waitForDeployment();
    const positionManagerAddress = await positionManager.getAddress();
    console.log("✅ PositionManager:", positionManagerAddress);

    // ========== 3. Wire Contracts ==========
    console.log("\n━━━ Step 3: Wiring Contracts ━━━");

    await vault.setMarket(marketAddress);
    console.log("✅ Vault.setMarket →", marketAddress);

    await market.setPositionManager(positionManagerAddress);
    console.log("✅ Market.setPositionManager →", positionManagerAddress);

    // ========== 4. Set Oracle Prices (Manual Mode) ==========
    console.log("\n━━━ Step 4: Setting Oracle Prices ━━━");

    // ETH = $3,450 (30 decimals)
    const ethPrice = ethers.parseUnits("3450", 30);
    await oracle.setManualPrice(wethAddress, ethPrice);
    console.log("✅ ETH price: $3,450");

    // BTC = $67,000 (30 decimals)
    const btcPrice = ethers.parseUnits("67000", 30);
    await oracle.setManualPrice(wbtcAddress, btcPrice);
    console.log("✅ BTC price: $67,000");

    // USDC = $1 (30 decimals)
    const usdcPrice = ethers.parseUnits("1", 30);
    await oracle.setManualPrice(usdcAddress, usdcPrice);
    console.log("✅ USDC price: $1");

    // Verify prices
    const readEthPrice = await oracle.getPrice(wethAddress);
    const readBtcPrice = await oracle.getPrice(wbtcAddress);
    const readUsdcPrice = await oracle.getPrice(usdcAddress);
    console.log("   Verify ETH:", ethers.formatUnits(readEthPrice, 30), "USD");
    console.log("   Verify BTC:", ethers.formatUnits(readBtcPrice, 30), "USD");
    console.log("   Verify USDC:", ethers.formatUnits(readUsdcPrice, 30), "USD");

    // ========== 5. Seed Vault Liquidity ==========
    console.log("\n━━━ Step 5: Seeding Vault Liquidity ━━━");

    // Mint USDC to deployer for vault liquidity
    const vaultUsdcAmount = ethers.parseUnits("500000", 18); // 500K USDC
    await mockUsdc.mint(deployer.address, vaultUsdcAmount);
    await mockUsdc.approve(vaultAddress, vaultUsdcAmount);
    await vault.addLiquidity(usdcAddress, vaultUsdcAmount);
    console.log("✅ Vault USDC liquidity: 500,000 USDC");

    // Mint WETH to deployer for vault liquidity
    const vaultWethAmount = ethers.parseUnits("500", 18); // 500 WETH
    await mockWeth.mint(deployer.address, vaultWethAmount);
    await mockWeth.approve(vaultAddress, vaultWethAmount);
    await vault.addLiquidity(wethAddress, vaultWethAmount);
    console.log("✅ Vault WETH liquidity: 500 WETH");

    // Mint WBTC to deployer for vault liquidity
    const vaultWbtcAmount = ethers.parseUnits("25", 8); // 25 WBTC
    await mockWbtc.mint(deployer.address, vaultWbtcAmount);
    await mockWbtc.approve(vaultAddress, vaultWbtcAmount);
    await vault.addLiquidity(wbtcAddress, vaultWbtcAmount);
    console.log("✅ Vault WBTC liquidity: 25 WBTC");

    // Verify vault state
    const poolUsdc = await vault.poolAmounts(usdcAddress);
    const poolWeth = await vault.poolAmounts(wethAddress);
    const poolWbtc = await vault.poolAmounts(wbtcAddress);
    console.log("   Pool USDC:", ethers.formatUnits(poolUsdc, 18));
    console.log("   Pool WETH:", ethers.formatUnits(poolWeth, 18));
    console.log("   Pool WBTC:", ethers.formatUnits(poolWbtc, 8));

    // ========== 6. Mint Test Tokens to User ==========
    console.log("\n━━━ Step 6: Minting Test Tokens ━━━");

    // Anvil default account #0
    const testUser = deployer.address;
    const userUsdcAmount = ethers.parseUnits("10000", 18); // 10K USDC
    await mockUsdc.mint(testUser, userUsdcAmount);
    const userBalance = await mockUsdc.balanceOf(testUser);
    console.log(`✅ User ${testUser} balance: ${ethers.formatUnits(userBalance, 18)} USDC`);

    // ========== 7. Save Deployment ==========
    console.log("\n━━━ Step 7: Saving Deployment ━━━");

    const deployment = {
        network: "localhost",
        chainId: 31337,
        deployer: deployer.address,
        timestamp: new Date().toISOString(),
        contracts: {
            MockUSDC: { address: usdcAddress, decimals: 18 },
            MockWETH: { address: wethAddress, decimals: 18 },
            MockWBTC: { address: wbtcAddress, decimals: 8 },
            PerpOracle: { address: oracleAddress },
            PerpVault: { address: vaultAddress },
            PerpMarket: { address: marketAddress },
            PositionManager: { address: positionManagerAddress },
        },
        prices: {
            ETH: "$3,450",
            BTC: "$67,000",
            USDC: "$1",
        },
        liquidity: {
            USDC: "500,000",
            WETH: "500",
            WBTC: "25",
        },
    };

    const deploymentsDir = path.join(SCRIPT_DIR, "..", "deployments");
    if (!fs.existsSync(deploymentsDir)) {
        fs.mkdirSync(deploymentsDir, { recursive: true });
    }

    const filePath = path.join(deploymentsDir, "perp-local.json");
    fs.writeFileSync(filePath, JSON.stringify(deployment, null, 2));
    console.log("✅ Saved to:", filePath);

    // ========== Summary ==========
    console.log("\n╔══════════════════════════════════════════╗");
    console.log("║          Deployment Complete!             ║");
    console.log("╚══════════════════════════════════════════╝");
    console.log("\n📋 Copy these addresses to contracts.ts:\n");
    console.log(`  MockUSDC:         '${usdcAddress}',`);
    console.log(`  MockWETH:         '${wethAddress}',`);
    console.log(`  MockWBTC:         '${wbtcAddress}',`);
    console.log(`  PerpOracle:       '${oracleAddress}',`);
    console.log(`  PerpVault:        '${vaultAddress}',`);
    console.log(`  PerpMarket:       '${marketAddress}',`);
    console.log(`  PositionManager:  '${positionManagerAddress}',`);
    console.log("\n🔗 MetaMask: Connect to http://127.0.0.1:8545 (chainId: 31337)");
    console.log("💰 Test account has 10,000 USDC ready for trading\n");
}

main().catch((error) => {
    console.error(error);
    process.exitCode = 1;
});
