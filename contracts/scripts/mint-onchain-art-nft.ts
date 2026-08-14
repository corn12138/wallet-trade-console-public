import { getHardhatEthers } from "../hardhat-runtime.js";
import { getNftOption, resolveDeploymentAddress } from "./onchain-art-nft.utils";

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  const network = await ethers.provider.getNetwork();
  const contractAddress = resolveDeploymentAddress(network.chainId);
  const to = getNftOption("to", deployer.address);
  const title = getNftOption("title", "Genesis Ticket");
  const caption = getNftOption("caption", "Minted from the AI-code workspace");
  const accentColor = getNftOption("accent", "#38bdf8");

  console.log("=== Mint OnchainArtworkNFT ===");
  console.log("Network:", network.name, Number(network.chainId));
  console.log("Contract:", contractAddress);
  console.log("Recipient:", to);

  const contract = await ethers.getContractAt("OnchainArtworkNFT", contractAddress);
  const nextTokenId = await contract.mintArtwork.staticCall(to, title, caption, accentColor);
  const tx = await contract.mintArtwork(to, title, caption, accentColor);
  const receipt = await tx.wait();
  const tokenUri = await contract.tokenURI(nextTokenId);

  console.log("Mint tx:", receipt?.hash || tx.hash);
  console.log("Token ID:", nextTokenId.toString());
  console.log("Token URI:", tokenUri);
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
