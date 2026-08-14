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
  const recipient = getNftOption("to", deployer.address);
  const title = getNftOption("title", "Genesis Ticket");
  const caption = getNftOption("caption", "Minted from the AI-code workspace");
  const accentColor = getNftOption("accent", "#38bdf8");

  console.log("=== Deploy And Mint OnchainArtworkNFT ===");
  console.log("Deployer:", deployer.address);
  console.log("Collection:", `${name} (${symbol})`);
  console.log("Recipient:", recipient);

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

  const outputPath = saveDeployment(network.chainId, {
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
  });

  const tokenId = await contract.mintArtwork.staticCall(recipient, title, caption, accentColor);
  const mintTx = await contract.mintArtwork(recipient, title, caption, accentColor);
  const receipt = await mintTx.wait();
  const tokenUri = await contract.tokenURI(tokenId);

  console.log("Contract:", contractAddress);
  console.log("Saved:", outputPath);
  console.log("Mint tx:", receipt?.hash || mintTx.hash);
  console.log("Token ID:", tokenId.toString());
  console.log("Token URI:", tokenUri);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
