import { getHardhatEthers } from "../hardhat-runtime.js";
import { getNftOption, saveDeployment } from "./onchain-art-nft.utils";

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  const name = getNftOption("name", "AI Mint Ticket");
  const symbol = getNftOption("symbol", "AMT");
  const description = getNftOption(
    "description",
    "An on-chain SVG ERC721 collection for testnet demos."
  );
  const canvasColor = getNftOption("canvas", "#0f172a");

  console.log("=== Deploy OnchainArtworkNFT ===");
  console.log("Deployer:", deployer.address);
  console.log("Collection:", `${name} (${symbol})`);

  const balance = await ethers.provider.getBalance(deployer.address);
  console.log("Balance:", ethers.formatEther(balance), "ETH");

  if (balance < ethers.parseEther("0.005")) {
    throw new Error("Insufficient balance. Need at least 0.005 ETH");
  }

  const Factory = await ethers.getContractFactory("OnchainArtworkNFT");
  const contract = await Factory.deploy(name, symbol, description, canvasColor);
  await contract.waitForDeployment();

  const contractAddress = await contract.getAddress();
  const network = await ethers.provider.getNetwork();

  const deployment = {
    network: network.name,
    chainId: Number(network.chainId),
    deployer: deployer.address,
    timestamp: new Date().toISOString(),
    contracts: {
      OnchainArtworkNFT: {
        address: contractAddress,
        name,
        symbol,
        description,
        canvasColor,
      },
    },
  };

  const outputPath = saveDeployment(network.chainId, deployment);

  console.log("Contract:", contractAddress);
  console.log("Saved:", outputPath);
  console.log("Verification is outside the active contracts toolchain.");
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
