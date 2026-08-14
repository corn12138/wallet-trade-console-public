import { getHardhatEthers } from "../hardhat-runtime.js";
import {
  getIpfsNftOption,
  normalizeIpfsUri,
  resolveIpfsDeploymentAddress,
} from "./ipfs-art-nft.utils";

async function main() {
  const ethers = await getHardhatEthers();
  const [deployer] = await ethers.getSigners();
  const network = await ethers.provider.getNetwork();
  const contractAddress = resolveIpfsDeploymentAddress(network.chainId);
  const to = getIpfsNftOption("to", deployer.address);
  const tokenUri = normalizeIpfsUri(getIpfsNftOption("token-uri"));

  console.log("=== Mint IpfsArtworkNFT ===");
  console.log("Network:", network.name, Number(network.chainId));
  console.log("Contract:", contractAddress);
  console.log("Recipient:", to);
  console.log("Token URI:", tokenUri);

  const contract = await ethers.getContractAt("IpfsArtworkNFT", contractAddress);
  const tokenId = await contract.mintWithTokenURI.staticCall(to, tokenUri);
  const tx = await contract.mintWithTokenURI(to, tokenUri);
  const receipt = await tx.wait();

  console.log("Mint tx:", receipt?.hash || tx.hash);
  console.log("Token ID:", tokenId.toString());
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
