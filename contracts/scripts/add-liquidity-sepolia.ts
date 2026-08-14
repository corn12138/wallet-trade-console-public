/**
 * Add liquidity for the currently deployed Sepolia AMM pair.
 *
 * The script reads addresses from deployments/sepolia.json instead of hardcoding them.
 */

import { getDeploymentAddress } from "@wallet-trade/shared/node";
import { getHardhatEthers } from "../hardhat-runtime.js";
import {
  persistDeploymentRecord,
  readDeploymentRecord,
} from "./deployment-artifacts.js";

interface SepoliaDeployment {
  network: string;
  chainId: number;
  deployer?: string;
  timestamp?: string;
  deployedAt?: string;
  liquidityAddedAt?: string;
  contracts: Record<string, string | { address?: string; [key: string]: unknown }>;
}

async function main() {
  const deployment = readDeploymentRecord<SepoliaDeployment>("sepolia.json");
  if (!deployment) {
    throw new Error("Missing sepolia.json. Run pnpm deploy:sepolia first.");
  }

  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  const factoryAddress = getDeploymentAddress(
    { contracts: deployment.contracts, sources: [] },
    "factory",
    "Factory"
  );
  const routerAddress = getDeploymentAddress(
    { contracts: deployment.contracts, sources: [] },
    "router",
    "Router"
  );
  const tokenAAddress = getDeploymentAddress(
    { contracts: deployment.contracts, sources: [] },
    "MockUSDC",
    "tokenA"
  );
  const tokenBAddress = getDeploymentAddress(
    { contracts: deployment.contracts, sources: [] },
    "MockWETH",
    "tokenB"
  );

  if (!factoryAddress || !routerAddress || !tokenAAddress || !tokenBAddress) {
    throw new Error("sepolia.json is missing AMM addresses");
  }

  console.log("=== Add Liquidity to Sepolia ===");
  console.log("Deployer:", deployer.address);

  const factory = await ethers.getContractAt("TradingPairFactory", factoryAddress);
  const router = await ethers.getContractAt("Router", routerAddress);
  const tokenA = await ethers.getContractAt("MockERC20", tokenAAddress);
  const tokenB = await ethers.getContractAt("MockERC20", tokenBAddress);

  const mintAmountA = ethers.parseUnits("100000", 18);
  const mintAmountB = ethers.parseUnits("100", 18);
  await (await tokenA.mint(deployer.address, mintAmountA)).wait();
  await (await tokenB.mint(deployer.address, mintAmountB)).wait();
  await (await tokenA.approve(routerAddress, ethers.MaxUint256)).wait();
  await (await tokenB.approve(routerAddress, ethers.MaxUint256)).wait();

  await (
    await router.addLiquidity(
      tokenAAddress,
      tokenBAddress,
      mintAmountA,
      mintAmountB,
      0,
      0,
      deployer.address,
      Math.floor(Date.now() / 1000) + 3600
    )
  ).wait();

  const pairAddress = await factory.getPair(tokenAAddress, tokenBAddress);
  const pair = await ethers.getContractAt("TradingPair", pairAddress);
  const [reserve0, reserve1] = await pair.getReserves();

  const nextDeployment: SepoliaDeployment = {
    ...deployment,
    liquidityAddedAt: new Date().toISOString(),
    contracts: {
      ...deployment.contracts,
      pair: pairAddress,
    },
  };

  const { contractsPath, sharedPath } = persistDeploymentRecord("sepolia.json", nextDeployment);

  console.log("Pair Address:", pairAddress);
  console.log("Reserve 0:", reserve0.toString());
  console.log("Reserve 1:", reserve1.toString());
  console.log(`Updated:\n  - ${contractsPath}\n  - ${sharedPath}`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
