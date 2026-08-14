import { getHardhatEthers } from "../hardhat-runtime.js";
import { getIpfsNftOption, saveIpfsDeployment } from "./ipfs-art-nft.utils";

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  const name = getIpfsNftOption("name", "AI Mint Ticket IPFS");
  const symbol = getIpfsNftOption("symbol", "AMTI");

  console.log("=== Deploy IpfsArtworkNFT ===");
  console.log("Deployer:", deployer.address);
  console.log("Collection:", `${name} (${symbol})`);

  const balance = await ethers.provider.getBalance(deployer.address);
  console.log("Balance:", ethers.formatEther(balance), "ETH");

  if (balance < ethers.parseEther("0.005")) {
    throw new Error("Insufficient balance. Need at least 0.005 ETH");
  }

  const Factory = await ethers.getContractFactory("IpfsArtworkNFT");
  const contract = await Factory.deploy(name, symbol);
  await contract.waitForDeployment();

  const contractAddress = await contract.getAddress();
  const network = await ethers.provider.getNetwork();
  const outputPath = saveIpfsDeployment(network.chainId, {
    network: network.name,
    chainId: Number(network.chainId),
    deployer: deployer.address,
    timestamp: new Date().toISOString(),
    contracts: {
      IpfsArtworkNFT: {
        address: contractAddress,
        name,
        symbol,
      },
    },
  });

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
