/**
 * Unified Sepolia deployment for the current web console.
 *
 * Coverage:
 * - AMM swap contracts and liquidity
 * - TokenFactory / launchpad
 * - staking contracts
 * - perpetual trading contracts
 * - NFT contracts
 *
 * Artifacts are mirrored into packages/shared automatically so web/api
 * pick up the latest deployment without a manual sync step.
 */

import { getHardhatEthers } from "../hardhat-runtime.js";
import { persistDeploymentRecord } from "./deployment-artifacts.js";
import { assertV2CompatibleRouter } from "./router-compatibility.js";

type DeployedToken = {
  contract: any;
  address: string;
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
    decimals,
  };
}

async function addLiquidity(
  router: any,
  factory: any,
  ethers: Awaited<ReturnType<typeof getHardhatEthers>>,
  deployerAddress: string,
  tokenA: DeployedToken,
  tokenB: DeployedToken,
  amountA: bigint,
  amountB: bigint
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
  const configuredLaunchpadRouter = process.env.LAUNCHPAD_DEX_ROUTER;
  if (!configuredLaunchpadRouter) {
    throw new Error(
      "Missing LAUNCHPAD_DEX_ROUTER. The project Router only supports ERC20 liquidity and cannot graduate launchpad tokens."
    );
  }
  const launchpadDexRouter = await assertV2CompatibleRouter(
    ethers.provider,
    configuredLaunchpadRouter,
    "LAUNCHPAD_DEX_ROUTER"
  );
  const [deployer] = await ethers.getSigners();

  console.log("╔══════════════════════════════════════════╗");
  console.log("║    Sepolia Unified Deploy for Web App   ║");
  console.log("╚══════════════════════════════════════════╝\n");

  console.log("Deployer:", deployer.address);
  const balance = await ethers.provider.getBalance(deployer.address);
  console.log("Balance:", ethers.formatEther(balance), "ETH\n");

  if (balance < ethers.parseEther("0.06")) {
    throw new Error("Insufficient balance. Need at least 0.06 Sepolia ETH");
  }

  console.log("1/8 Deploying shared mock tokens...");
  const mockUsdc = await deployMockToken(ethers, "Mock USDC", "USDC", 18);
  const mockWeth = await deployMockToken(ethers, "Mock WETH", "WETH", 18);
  const mockWbtc = await deployMockToken(ethers, "Mock WBTC", "WBTC", 8);

  console.log("2/8 Deploying AMM...");
  const TradingPairFactory = await ethers.getContractFactory("TradingPairFactory");
  const factory = await TradingPairFactory.deploy(deployer.address);
  await factory.waitForDeployment();
  const factoryAddress = await factory.getAddress();

  const Router = await ethers.getContractFactory("Router");
  const router = await Router.deploy(factoryAddress, mockWeth.address);
  await router.waitForDeployment();
  const routerAddress = await router.getAddress();

  await (await mockUsdc.contract.mint(deployer.address, ethers.parseUnits("3000000", 18))).wait();
  await (await mockWeth.contract.mint(deployer.address, ethers.parseUnits("3000", 18))).wait();
  await (await mockWbtc.contract.mint(deployer.address, ethers.parseUnits("150", 8))).wait();

  const primaryPairAddress = await addLiquidity(
    router,
    factory,
    ethers,
    deployer.address,
    mockUsdc,
    mockWeth,
    ethers.parseUnits("600000", 18),
    ethers.parseUnits("600", 18)
  );
  await addLiquidity(
    router,
    factory,
    ethers,
    deployer.address,
    mockUsdc,
    mockWbtc,
    ethers.parseUnits("300000", 18),
    ethers.parseUnits("15", 8)
  );
  await addLiquidity(
    router,
    factory,
    ethers,
    deployer.address,
    mockWeth,
    mockWbtc,
    ethers.parseUnits("200", 18),
    ethers.parseUnits("6", 8)
  );

  console.log("3/8 Deploying launchpad...");
  const creationFee = ethers.parseEther("0.001");
  const TokenFactory = await ethers.getContractFactory("TokenFactory");
  const tokenFactory = await TokenFactory.deploy(
    creationFee,
    deployer.address,
    launchpadDexRouter
  );
  await tokenFactory.waitForDeployment();
  const tokenFactoryAddress = await tokenFactory.getAddress();

  console.log("4/8 Deploying staking contracts...");
  const MockERC20 = await ethers.getContractFactory("MockERC20");
  const stakingToken = await MockERC20.deploy("Staking Token", "STK", 18);
  await stakingToken.waitForDeployment();
  const stakingTokenAddress = await stakingToken.getAddress();
  await (await stakingToken.mint(deployer.address, ethers.parseUnits("1000000", 18))).wait();

  const StakingPool = await ethers.getContractFactory("StakingPool");
  const rewardsDuration = 30 * 24 * 60 * 60;
  const stakingPool = await StakingPool.deploy(
    stakingTokenAddress,
    stakingTokenAddress,
    rewardsDuration
  );
  await stakingPool.waitForDeployment();
  const stakingPoolAddress = await stakingPool.getAddress();

  console.log("5/8 Deploying perpetual trading contracts...");
  const PerpOracle = await ethers.getContractFactory("PerpOracle");
  const oracle = await PerpOracle.deploy();
  await oracle.waitForDeployment();
  const oracleAddress = await oracle.getAddress();

  const PerpVault = await ethers.getContractFactory("PerpVault");
  const vault = await PerpVault.deploy();
  await vault.waitForDeployment();
  const vaultAddress = await vault.getAddress();

  const PerpMarket = await ethers.getContractFactory("PerpMarket");
  const market = await PerpMarket.deploy(vaultAddress, oracleAddress, deployer.address);
  await market.waitForDeployment();
  const marketAddress = await market.getAddress();

  const PositionManager = await ethers.getContractFactory("PositionManager");
  const positionManager = await PositionManager.deploy(marketAddress, oracleAddress);
  await positionManager.waitForDeployment();
  const positionManagerAddress = await positionManager.getAddress();

  await (await vault.setMarket(marketAddress)).wait();
  await (await market.setPositionManager(positionManagerAddress)).wait();

  console.log("6/8 Seeding perpetual prices and liquidity...");
  await (await oracle.setManualPrice(mockWeth.address, ethers.parseUnits("3450", 30))).wait();
  await (await oracle.setManualPrice(mockWbtc.address, ethers.parseUnits("67000", 30))).wait();
  await (await oracle.setManualPrice(mockUsdc.address, ethers.parseUnits("1", 30))).wait();

  await (await mockUsdc.contract.approve(vaultAddress, ethers.MaxUint256)).wait();
  await (await mockWeth.contract.approve(vaultAddress, ethers.MaxUint256)).wait();
  await (await mockWbtc.contract.approve(vaultAddress, ethers.MaxUint256)).wait();
  await (await vault.addLiquidity(mockUsdc.address, ethers.parseUnits("500000", 18))).wait();
  await (await vault.addLiquidity(mockWeth.address, ethers.parseUnits("500", 18))).wait();
  await (await vault.addLiquidity(mockWbtc.address, ethers.parseUnits("25", 8))).wait();
  await (await mockUsdc.contract.mint(deployer.address, ethers.parseUnits("10000", 18))).wait();

  console.log("7/8 Deploying NFT contracts...");
  const OnchainArtworkNFT = await ethers.getContractFactory("OnchainArtworkNFT");
  const onchainArtworkNft = await OnchainArtworkNFT.deploy(
    "AI Mint Ticket",
    "AMT",
    "An on-chain SVG ERC721 collection for testnet demos.",
    "#0f172a"
  );
  await onchainArtworkNft.waitForDeployment();
  const onchainArtworkNftAddress = await onchainArtworkNft.getAddress();

  const IpfsArtworkNFT = await ethers.getContractFactory("IpfsArtworkNFT");
  const ipfsArtworkNft = await IpfsArtworkNFT.deploy("AI Mint Ticket IPFS", "AMTI");
  await ipfsArtworkNft.waitForDeployment();
  const ipfsArtworkNftAddress = await ipfsArtworkNft.getAddress();

  console.log("8/8 Writing deployment artifacts...");
  const timestamp = new Date().toISOString();

  const ammDeployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp,
    contracts: {
      factory: factoryAddress,
      router: routerAddress,
      weth: mockWeth.address,
      tokenA: mockUsdc.address,
      tokenB: mockWeth.address,
      pair: primaryPairAddress,
      MockUSDC: { address: mockUsdc.address, decimals: mockUsdc.decimals },
      MockWETH: { address: mockWeth.address, decimals: mockWeth.decimals },
      MockWBTC: { address: mockWbtc.address, decimals: mockWbtc.decimals },
    },
  };

  const launchpadDeployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp,
    contracts: {
      TokenFactory: {
        address: tokenFactoryAddress,
        creationFee: creationFee.toString(),
        feeRecipient: deployer.address,
        dexRouter: launchpadDexRouter,
      },
      StakingToken: {
        address: stakingTokenAddress,
        symbol: "STK",
        decimals: 18,
      },
      StakingPool: {
        address: stakingPoolAddress,
        rewardsDuration,
      },
    },
  };

  const onchainDeployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp,
    contracts: {
      OnchainArtworkNFT: {
        address: onchainArtworkNftAddress,
        name: "AI Mint Ticket",
        symbol: "AMT",
      },
    },
  };

  const ipfsDeployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp,
    contracts: {
      IpfsArtworkNFT: {
        address: ipfsArtworkNftAddress,
        name: "AI Mint Ticket IPFS",
        symbol: "AMTI",
      },
    },
  };

  const allDeployment = {
    network: "sepolia",
    chainId: 11155111,
    deployer: deployer.address,
    timestamp,
    contracts: {
      ...ammDeployment.contracts,
      ...launchpadDeployment.contracts,
      PerpOracle: { address: oracleAddress },
      PerpVault: { address: vaultAddress },
      PerpMarket: { address: marketAddress },
      PositionManager: { address: positionManagerAddress },
      ...onchainDeployment.contracts,
      ...ipfsDeployment.contracts,
    },
  };

  persistDeploymentRecord("sepolia.json", ammDeployment);
  persistDeploymentRecord("launchpad-sepolia.json", launchpadDeployment);
  persistDeploymentRecord("onchain-art-nft-sepolia.json", onchainDeployment);
  persistDeploymentRecord("ipfs-art-nft-sepolia.json", ipfsDeployment);
  const { contractsPath, sharedPath } = persistDeploymentRecord(
    "all-sepolia.json",
    allDeployment
  );

  console.log("\nDeployment complete.");
  console.log("Artifacts:");
  console.log("  - contracts/deployments/*.json");
  console.log("  - packages/shared/deployments/*.json");
  console.log("  - packages/shared/src/web3/contract-addresses.generated.ts");
  console.log(`\nPrimary files:\n  - ${contractsPath}\n  - ${sharedPath}`);
  console.log("\nKey addresses:");
  console.log(`  Router: ${routerAddress}`);
  console.log(`  TokenFactory: ${tokenFactoryAddress}`);
  console.log(`  PositionManager: ${positionManagerAddress}`);
  console.log(`  OnchainArtworkNFT: ${onchainArtworkNftAddress}`);
  console.log(`  IpfsArtworkNFT: ${ipfsArtworkNftAddress}`);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error("Deployment failed:", error);
    process.exit(1);
  });
