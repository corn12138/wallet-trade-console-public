import { getDeploymentAddress, loadMergedDeployments } from "@wallet-trade/shared/node";
import { getHardhatEthers } from "../hardhat-runtime.js";
import {
    getContractsDeploymentsDir,
    persistDeploymentRecord,
} from "./deployment-artifacts.js";

/**
 * Deploy Launchpad contracts to Sepolia
 * - TokenFactory: Main factory for creating tokens with bonding curves
 */
async function main() {
    console.log("🚀 Deploying Launchpad contracts to Sepolia...\n");

    const ethers = await getHardhatEthers();
    const [deployer] = await ethers.getSigners();
    console.log("Deploying with account:", deployer.address);

    const balance = await ethers.provider.getBalance(deployer.address);
    console.log("Account balance:", ethers.formatEther(balance), "ETH\n");

    // Configuration
    const CREATION_FEE = ethers.parseEther("0.001"); // 0.001 ETH creation fee
    const FEE_RECIPIENT = deployer.address; // Can be changed later
    const existingDeployments = loadMergedDeployments(getContractsDeploymentsDir());
    const resolvedDexRouter =
        process.env.LAUNCHPAD_DEX_ROUTER
        || getDeploymentAddress(existingDeployments[11155111], "router", "Router");

    if (!resolvedDexRouter) {
        throw new Error("Missing LAUNCHPAD_DEX_ROUTER. Deploy AMM first or set the env var.");
    }

    // Deploy TokenFactory
    console.log("📦 Deploying TokenFactory...");
    const TokenFactory = await ethers.getContractFactory("TokenFactory");
    const tokenFactory = await TokenFactory.deploy(
        CREATION_FEE,
        FEE_RECIPIENT,
        resolvedDexRouter
    );
    await tokenFactory.waitForDeployment();

    const factoryAddress = await tokenFactory.getAddress();
    console.log("✅ TokenFactory deployed to:", factoryAddress);

    // Log configuration
    console.log("\n📋 TokenFactory Configuration:");
    console.log("  - Creation Fee:", ethers.formatEther(CREATION_FEE), "ETH");
    console.log("  - Fee Recipient:", FEE_RECIPIENT);
    console.log("  - DEX Router:", resolvedDexRouter);
    console.log("  - Default Base Price:", ethers.formatEther(await tokenFactory.defaultBasePrice()), "ETH");
    console.log("  - Default Trade Fee:", (await tokenFactory.defaultTradeFee()).toString(), "basis points");

    // Save deployment info
    const deploymentInfo = {
        network: "sepolia",
        chainId: 11155111,
        deployer: deployer.address,
        contracts: {
            TokenFactory: {
                address: factoryAddress,
                creationFee: CREATION_FEE.toString(),
                feeRecipient: FEE_RECIPIENT,
                dexRouter: resolvedDexRouter,
            },
        },
        timestamp: new Date().toISOString(),
    };

    const { contractsPath, sharedPath } = persistDeploymentRecord(
        "launchpad-sepolia.json",
        deploymentInfo
    );

    console.log(`\n💾 Deployment info saved to:\n  - ${contractsPath}\n  - ${sharedPath}`);
    console.log("\n🎉 Deployment complete!");
    console.log("\n📝 Next steps:");
    console.log("  1. Start web/api and verify create-token flow reads the new TokenFactory address");
    console.log("  2. If explorer verification is required, run it in a dedicated verification environment");
}

main()
    .then(() => process.exit(0))
    .catch((error) => {
        console.error("❌ Deployment failed:", error);
        process.exit(1);
    });
