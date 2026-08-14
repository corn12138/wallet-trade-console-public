import { expect } from "chai";
import { getHardhatEthers } from "../hardhat-runtime.js";

describe("IpfsArtworkNFT", function () {
  async function deployFixture() {
    const ethers = await getHardhatEthers();
    const [owner, recipient, other] = await ethers.getSigners();
    const Factory = await ethers.getContractFactory("IpfsArtworkNFT");
    const contract = await Factory.deploy("AI Mint Ticket IPFS", "AMTI");
    await contract.waitForDeployment();

    return { contract, owner, recipient, other };
  }

  it("mints an ERC721 token with ipfs metadata", async function () {
    const { contract, recipient } = await deployFixture();
    const tokenUri = "ipfs://QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2";

    await contract.mintWithTokenURI(recipient.address, tokenUri);

    expect(await contract.ownerOf(1)).to.equal(recipient.address);
    expect(await contract.tokenURI(1)).to.equal(tokenUri);
  });

  it("requires a non-empty token URI", async function () {
    const { contract, recipient } = await deployFixture();

    await expect(contract.mintWithTokenURI(recipient.address, "")).to.be.revertedWith(
      "Token URI required"
    );
  });

  it("restricts minting to the owner", async function () {
    const ethers = await getHardhatEthers();
    const { contract, recipient, other } = await deployFixture();
    const tokenUri = "ipfs://QmNpC4Rh3jv8jPDhnoNehtFVpqgzmEEGEaACcXaSMm7De2";

    await expect(
      contract.connect(other).mintWithTokenURI(recipient.address, tokenUri)
    ).to.revert(ethers);
  });
});
