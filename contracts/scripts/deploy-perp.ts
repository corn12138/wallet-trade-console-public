import { getHardhatEthers } from "../hardhat-runtime.js";

async function main() {
    const ethers = await getHardhatEthers();
    const [deployer] = await ethers.getSigners();
    console.log("Deploying contracts with the account:", deployer.address);

    // 1. Deploy Oracle
    console.log("Deploying PerpOracle...");
    const PerpOracle = await ethers.getContractFactory("PerpOracle");
    const oracle = await PerpOracle.deploy();
    await oracle.waitForDeployment();
    const oracleAddress = await oracle.getAddress();
    console.log("PerpOracle deployed to:", oracleAddress);

    // 2. Deploy Vault
    console.log("Deploying PerpVault...");
    const PerpVault = await ethers.getContractFactory("PerpVault");
    const vault = await PerpVault.deploy();
    await vault.waitForDeployment();
    const vaultAddress = await vault.getAddress();
    console.log("PerpVault deployed to:", vaultAddress);

    // 3. Deploy Market
    console.log("Deploying PerpMarket...");
    const PerpMarket = await ethers.getContractFactory("PerpMarket");
    // Constructor: vault, oracle
    const market = await PerpMarket.deploy(vaultAddress, oracleAddress);
    await market.waitForDeployment();
    const marketAddress = await market.getAddress();
    console.log("PerpMarket deployed to:", marketAddress);

    // 4. Deploy PositionManager
    console.log("Deploying PositionManager...");
    const PositionManager = await ethers.getContractFactory("PositionManager");
    // Constructor: market, oracle
    const positionManager = await PositionManager.deploy(marketAddress, oracleAddress);
    await positionManager.waitForDeployment();
    const positionManagerAddress = await positionManager.getAddress();
    console.log("PositionManager deployed to:", positionManagerAddress);

    // 5. Config / Wiring
    console.log("Configuring contracts...");

    // Vault needs to set Market
    await vault.connect(deployer).setMarket(marketAddress);
    console.log("Vault market set to:", marketAddress);

    // Market needs to set PositionManager
    await market.connect(deployer).setPositionManager(positionManagerAddress);
    console.log("Market positionManager set to:", positionManagerAddress);

    console.log("Deployment complete!");
    console.log("----------------------------------------------------");
    console.log(`PerpOracle:       ${oracleAddress}`);
    console.log(`PerpVault:        ${vaultAddress}`);
    console.log(`PerpMarket:       ${marketAddress}`);
    console.log(`PositionManager:  ${positionManagerAddress}`);
    console.log("----------------------------------------------------");
}

main().catch((error) => {
    console.error(error);
    process.exitCode = 1;
});
