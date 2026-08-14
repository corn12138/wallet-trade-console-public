import { getHardhatEthers } from "../hardhat-runtime.js";

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  console.log("Deploying contracts with:", deployer.address);
  console.log("Account balance:", (await ethers.provider.getBalance(deployer.address)).toString());

  // 1. Deploy Factory
  const TradingPairFactory = await ethers.getContractFactory("TradingPairFactory");
  const factory = await TradingPairFactory.deploy(deployer.address);
  await factory.waitForDeployment();
  console.log("Factory deployed to:", await factory.getAddress());

  // 2. Deploy Test Tokens
  const MockWETH = await ethers.getContractFactory("MockWETH");
  const weth = await MockWETH.deploy();
  await weth.waitForDeployment();
  console.log("WETH deployed to:", await weth.getAddress());

  const MockUSDT = await ethers.getContractFactory("MockUSDT");
  const usdt = await MockUSDT.deploy();
  await usdt.waitForDeployment();
  console.log("USDT deployed to:", await usdt.getAddress());

  const MockUSDC = await ethers.getContractFactory("MockUSDC");
  const usdc = await MockUSDC.deploy();
  await usdc.waitForDeployment();
  console.log("USDC deployed to:", await usdc.getAddress());

  const MockWBTC = await ethers.getContractFactory("MockWBTC");
  const wbtc = await MockWBTC.deploy();
  await wbtc.waitForDeployment();
  console.log("WBTC deployed to:", await wbtc.getAddress());

  const MockDAI = await ethers.getContractFactory("MockDAI");
  const dai = await MockDAI.deploy();
  await dai.waitForDeployment();
  console.log("DAI deployed to:", await dai.getAddress());

  // 3. Deploy Router
  const Router = await ethers.getContractFactory("Router");
  const router = await Router.deploy(await factory.getAddress(), await weth.getAddress());
  await router.waitForDeployment();
  console.log("Router deployed to:", await router.getAddress());

  // 4. Create Trading Pairs
  const wethUsdtTx = await factory.createPair(await weth.getAddress(), await usdt.getAddress());
  await wethUsdtTx.wait();
  console.log("WETH/USDT pair created");

  const wethUsdcTx = await factory.createPair(await weth.getAddress(), await usdc.getAddress());
  await wethUsdcTx.wait();
  console.log("WETH/USDC pair created");

  const wbtcUsdtTx = await factory.createPair(await wbtc.getAddress(), await usdt.getAddress());
  await wbtcUsdtTx.wait();
  console.log("WBTC/USDT pair created");

  // 5. Mint test tokens to deployer
  await weth.mint(deployer.address, ethers.parseEther("1000"));
  await usdt.mint(deployer.address, ethers.parseUnits("10000000", 6)); // 10M USDT
  await usdc.mint(deployer.address, ethers.parseUnits("10000000", 6)); // 10M USDC
  await wbtc.mint(deployer.address, ethers.parseUnits("100", 8)); // 100 WBTC
  await dai.mint(deployer.address, ethers.parseEther("10000000")); // 10M DAI
  console.log("Minted test tokens to:", deployer.address);

  // 6. Approve Router for all tokens
  const maxApproval = ethers.MaxUint256;
  await weth.approve(await router.getAddress(), maxApproval);
  await usdt.approve(await router.getAddress(), maxApproval);
  await usdc.approve(await router.getAddress(), maxApproval);
  await wbtc.approve(await router.getAddress(), maxApproval);

  // 7. Add initial liquidity
  const deadline = Math.floor(Date.now() / 1000) + 3600;

  // WETH/USDT liquidity (1 ETH = 2000 USDT)
  await router.addLiquidity(
    await weth.getAddress(),
    await usdt.getAddress(),
    ethers.parseEther("100"),
    ethers.parseUnits("200000", 6),
    0, 0,
    deployer.address,
    deadline
  );
  console.log("Added WETH/USDT liquidity");

  // WETH/USDC liquidity (1 ETH = 2000 USDC)
  await router.addLiquidity(
    await weth.getAddress(),
    await usdc.getAddress(),
    ethers.parseEther("100"),
    ethers.parseUnits("200000", 6),
    0, 0,
    deployer.address,
    deadline
  );
  console.log("Added WETH/USDC liquidity");

  // WBTC/USDT liquidity (1 BTC = 40000 USDT)
  await router.addLiquidity(
    await wbtc.getAddress(),
    await usdt.getAddress(),
    ethers.parseUnits("10", 8),
    ethers.parseUnits("400000", 6),
    0, 0,
    deployer.address,
    deadline
  );
  console.log("Added WBTC/USDT liquidity");

  // Output deployment summary
  console.log("\n=== Deployment Summary ===");
  console.log("Factory:", await factory.getAddress());
  console.log("Router:", await router.getAddress());
  console.log("WETH:", await weth.getAddress());
  console.log("USDT:", await usdt.getAddress());
  console.log("USDC:", await usdc.getAddress());
  console.log("WBTC:", await wbtc.getAddress());
  console.log("DAI:", await dai.getAddress());
  console.log("==========================\n");
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
