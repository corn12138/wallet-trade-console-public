import * as fs from "fs";
import * as path from "path";
import { getIpfsNftOption, normalizeIpfsUri } from "./ipfs-art-nft.utils";
import { getScriptDir } from "./runtime-paths.js";

const SCRIPT_DIR = getScriptDir(import.meta.url);

type MetadataAttribute = {
  trait_type: string;
  value: string | number | boolean;
};

type NftMetadata = {
  name: string;
  description: string;
  image: string;
  external_url?: string;
  attributes?: MetadataAttribute[];
};

function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function parseAttributes(input: string): MetadataAttribute[] | undefined {
  if (!input.trim()) {
    return undefined;
  }

  const parsed = JSON.parse(input) as MetadataAttribute[];
  if (!Array.isArray(parsed)) {
    throw new Error("attributes must be a JSON array");
  }

  return parsed;
}

async function main() {
  const name = getIpfsNftOption("name", "AI Mint Ticket Genesis");
  const description = getIpfsNftOption(
    "description",
    "An IPFS-backed ERC721 minted from the AI-code workspace."
  );
  const image = normalizeIpfsUri(getIpfsNftOption("image"));
  const externalUrl = getIpfsNftOption("external-url");
  const attributes = parseAttributes(getIpfsNftOption("attributes", ""));
  const outputArg = getIpfsNftOption("out");

  const metadata: NftMetadata = {
    name,
    description,
    image,
  };

  if (externalUrl) {
    metadata.external_url = externalUrl;
  }

  if (attributes?.length) {
    metadata.attributes = attributes;
  }

  const defaultOutput = path.join(
    SCRIPT_DIR,
    "..",
    "metadata",
    `${slugify(name) || "ipfs-artwork"}.json`
  );
  const outputPath = path.resolve(outputArg || defaultOutput);
  const outputDir = path.dirname(outputPath);

  if (!fs.existsSync(outputDir)) {
    fs.mkdirSync(outputDir, { recursive: true });
  }

  fs.writeFileSync(outputPath, JSON.stringify(metadata, null, 2));

  console.log("Saved metadata:", outputPath);
  console.log("Metadata preview:", JSON.stringify(metadata, null, 2));
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error(error);
    process.exit(1);
  });
