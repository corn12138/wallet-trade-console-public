import { expect } from "chai";
import { getHardhatEthers } from "../hardhat-runtime.js";

describe("OnchainArtworkNFT", function () {
  async function deployFixture() {
    const ethers = await getHardhatEthers();
    const [owner, recipient, other] = await ethers.getSigners();
    const Factory = await ethers.getContractFactory("OnchainArtworkNFT");
    const contract = await Factory.deploy(
      "AI Mint Ticket",
      "AMT",
      "An on-chain SVG NFT for testnet demos.",
      "#0f172a"
    );
    await contract.waitForDeployment();

    return { contract, owner, recipient, other };
  }

  it("mints an on-chain artwork NFT and returns a data URI", async function () {
    const { contract, recipient } = await deployFixture();

    await contract.mintArtwork(
      recipient.address,
      "Genesis Ticket",
      "Minted in test",
      "#38bdf8"
    );

    expect(await contract.ownerOf(1)).to.equal(recipient.address);

    const tokenUri = await contract.tokenURI(1);
    expect(tokenUri.startsWith("data:application/json;base64,")).to.equal(true);

    const encodedJson = tokenUri.replace("data:application/json;base64,", "");
    const decodedJson = Buffer.from(encodedJson, "base64").toString("utf8");
    expect(decodedJson).to.contain("Genesis Ticket #1");
    expect(decodedJson).to.contain("data:image/svg+xml;base64,");
    expect(decodedJson).to.contain('"trait_type":"Accent"');
  });

  it("restricts minting to the owner", async function () {
    const ethers = await getHardhatEthers();
    const { contract, recipient, other } = await deployFixture();

    await expect(
      contract.connect(other).mintArtwork(
        recipient.address,
        "Unauthorized",
        "Should fail",
        "#ef4444"
      )
    ).to.revert(ethers);
  });

  it("escapes JSON and SVG text in token metadata", async function () {
    const { contract, recipient } = await deployFixture();

    await contract.mintArtwork(
      recipient.address,
      'Genesis "Ticket" <One>',
      'Minted & signed by "AI"',
      "#22c55e"
    );

    const tokenUri = await contract.tokenURI(1);
    const encodedJson = tokenUri.replace("data:application/json;base64,", "");
    const decodedJson = Buffer.from(encodedJson, "base64").toString("utf8");
    const metadata = JSON.parse(decodedJson) as { name: string; description: string; image: string };
    const encodedSvg = metadata.image.replace("data:image/svg+xml;base64,", "");
    const decodedSvg = Buffer.from(encodedSvg, "base64").toString("utf8");

    expect(metadata.name).to.equal('Genesis "Ticket" <One> #1');
    expect(metadata.description).to.contain('Minted & signed by "AI"');
    expect(decodedSvg).to.contain("Genesis \"Ticket\" &lt;One&gt;");
    expect(decodedSvg).to.contain("Minted &amp; signed by \"AI\"");
  });
});
