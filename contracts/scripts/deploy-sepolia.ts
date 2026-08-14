/**
 * Sepolia AMM deployment for the current web console.
 *
 * This script deploys:
 * - TradingPairFactory
 * - Router
 * - MockUSDC / MockWETH / MockWBTC
 * - liquidity for all token pairs used by the swap UI
 *
 * Deployment artifacts are written to both:
 * - contracts/deployments
 * - packages/shared/deployments
 */

import { getHardhatEthers } from "../hardhat-runtime.js";
import { persistDeploymentRecord } from "./deployment-artifacts.js";

type DeployedToken = {
  contract: any;
  address: string;
  symbol: string;
  decimals: number;
};

async function deployMockToken(
  ethers: Awaited<ReturnType<typeof getHardhatEthers>>,
  name: string,
  symbol: string,
  decimals: number
): Promise<DeployedToken> {
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const contract = await MockERC20.deploy(name, symbol, decimals);
  await contract.waitForDeployment();

  return {
    contract,
    address: await contract.getAddress(),
    symbol,
    decimals,
  };
}

async function addLiquidity(
  router: any,
  factory: any,
  tokenA: DeployedToken,
  tokenB: DeployedToken,
  amountA: bigint,
  amountB: bigint,
  deployerAddress: string,
  ethers: Awaited<ReturnType<typeof getHardhatEthers>>
) {
  await (await tokenA.contract.approve(await router.getAddress(), ethers.MaxUint256)).wait();
  await (await tokenB.contract.approve(await router.getAddress(), ethers.MaxUint256)).wait();

  await (
    await router.addLiquidity(
      tokenA.address,
      tokenB.address,
      amountA,
      amountB,
      0,
      0,
      deployerAddress,
      Math.floor(Date.now() / 1000) + 3600
    )
  ).wait();

  return factory.getPair(tokenA.address, tokenB.address);
}

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();

  console.log("=== Sepolia AMM Deployment ===");
  console.log("Deployer:", deployer.address);

  const balance = await ethers.provider.getBalance(deployer.address);
  console.log("Balance:", ethers.formatEther(balance), "ETH");

  if (balance < ethers.parseEther("0.02")) {
    throw new Error("Insufficient balance. Need at least 0.02 Sepolia ETH");
  }

  console.log("\n1. Deploying mock trading tokens...");
  const mockUsdc = await deployMockToken(ethers, "Mock USDC", "USDC", 18);
  const mockWeth = await deployMockToken(ethers, "Mock WETH", "WETH", 18);
  const mockWbtc = await deployMockToken(ethers, "Mock WBTC", "WBTC", 8);
  console.log("   MockUSDC:", mockUsdc.address);
  console.log("   MockWETH:", mockWeth.address);
  console.log("   MockWBTC:", mockWbtc.address);

  console.log("\n2. Deploying AMM factory and router...");
  const TradingPairFactory = await ethers.getContractFactory("TradingPairFactory");
  const factory = await TradingPairFactory.deploy(deployer.address);
  await factory.waitForDeployment();
  const factoryAddress = await factory.getAddress();

  const Router = await ethers.getContractFactory("Router");
  const router = await Router.deploy(factoryAddress, mockWeth.address);
  await router.waitForDeployment();
  const routerAddress = await router.getAddress();
  console.log("   Factory:", factoryAddress);
  console.log("   Router:", routerAddress);

  console.log("\n3. Minting swap inventory...");
  await (await mockUsdc.contract.mint(deployer.address, ethers.parseUnits("2000000", 18))).wait();
  await (await mockWeth.contract.mint(deployer.address, ethers.parseUnits("2000", 18))).wait();
  await (await mockWbtc.contract.mint(deployer.address, ethers.parseUnits("100", 8))).wait();
  console.log("   Minted liquidity inventory to deployer");

  console.log("\n4. Adding AMM liquidity...");
  const usdcWethPair = await addLiquidity(
    router,
    factory,
    mockUsdc,
    mockWeth,
    ethers.parseUnits("500000", 18),
    ethers.parseUnits("500", 18),
    deployer.address,
    ethers
  );
  await addLiquidity(
    router,
    factory,
    mockUsdc,
    mockWbtc,
    ethers.parseUnits("250000", 18),
    ethers.parseUnits("12", 8),
    deployer.address,
    ethers
  );
  await addLiquidity(
    router,
    factory,
    mockWeth,
    mockWbtc,
    ethers.parseUnits("200", 18),
    ethers.parseUnits("6", 8),
    deployer.address,
    ethers
  );
  console.log("   Primary pair (mUSDC/mWETH):", usdcWethPair);

  const deployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp: new Date().toISOString(),
    contracts: {
      factory: factoryAddress,
      router: routerAddress,
      weth: mockWeth.address,
      tokenA: mockUsdc.address,
      tokenB: mockWeth.address,
      pair: usdcWethPair,
      MockUSDC: {
        address: mockUsdc.address,
        decimals: mockUsdc.decimals,
      },
      MockWETH: {
        address: mockWeth.address,
        decimals: mockWeth.decimals,
      },
      MockWBTC: {
        address: mockWbtc.address,
        decimals: mockWbtc.decimals,
      },
    },
  };

  const { contractsPath, sharedPath } = persistDeploymentRecord("sepolia.json", deployment);

  console.log(`\n📁 Deployment saved to:\n  - ${contractsPath}\n  - ${sharedPath}`);
  console.log("\nSwap-ready token set:");
  console.log(`  mUSDC: ${mockUsdc.address}`);
  console.log(`  mWETH: ${mockWeth.address}`);
  console.log(`  mWBTC: ${mockWbtc.address}`);
  console.log(`  Router: ${routerAddress}`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
