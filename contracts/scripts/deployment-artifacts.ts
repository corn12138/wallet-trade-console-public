import * as fs from "node:fs";
import * as path from "node:path";
import {
  buildGeneratedContractAddressesSource,
  loadMergedDeployments,
} from "@wallet-trade/shared/node";
import { getScriptDir } from "./runtime-paths.js";

const SCRIPT_DIR = getScriptDir(import.meta.url);
const CONTRACTS_DEPLOYMENTS_DIR = path.resolve(SCRIPT_DIR, "..", "deployments");
const SHARED_DEPLOYMENTS_DIR = path.resolve(
  SCRIPT_DIR,
  "..",
  "..",
  "packages",
  "shared",
  "deployments"
);
const SHARED_GENERATED_ADDRESSES_PATH = path.resolve(
  SCRIPT_DIR,
  "..",
  "..",
  "packages",
  "shared",
  "src",
  "web3",
  "contract-addresses.generated.ts"
);

interface DeploymentRecord {
  network?: string;
  chainId?: number;
  deployer?: string;
  timestamp?: string;
  deployedAt?: string;
  contracts?: Record<string, unknown>;
  [key: string]: unknown;
}

function ensureDir(dirPath: string) {
  if (!fs.existsSync(dirPath)) {
    fs.mkdirSync(dirPath, { recursive: true });
  }
}

function writeJson(filePath: string, data: DeploymentRecord) {
  ensureDir(path.dirname(filePath));
  fs.writeFileSync(filePath, `${JSON.stringify(data, null, 2)}\n`);
}

export function getContractsDeploymentsDir(): string {
  ensureDir(CONTRACTS_DEPLOYMENTS_DIR);
  return CONTRACTS_DEPLOYMENTS_DIR;
}

export function getSharedDeploymentsDir(): string {
  ensureDir(SHARED_DEPLOYMENTS_DIR);
  return SHARED_DEPLOYMENTS_DIR;
}

export function getContractsDeploymentPath(fileName: string): string {
  return path.join(getContractsDeploymentsDir(), fileName);
}

export function getSharedDeploymentPath(fileName: string): string {
  return path.join(getSharedDeploymentsDir(), fileName);
}

export function readDeploymentRecord<T extends DeploymentRecord = DeploymentRecord>(
  fileName: string
): T | null {
  const candidatePaths = [
    getContractsDeploymentPath(fileName),
    getSharedDeploymentPath(fileName),
  ];

  for (const filePath of candidatePaths) {
    if (!fs.existsSync(filePath)) {
      continue;
    }

    return JSON.parse(fs.readFileSync(filePath, "utf8")) as T;
  }

  return null;
}

export function persistDeploymentRecord(
  fileName: string,
  data: DeploymentRecord
): { contractsPath: string; sharedPath: string } {
  const contractsPath = getContractsDeploymentPath(fileName);
  const sharedPath = getSharedDeploymentPath(fileName);

  writeJson(contractsPath, data);
  writeJson(sharedPath, data);
  syncGeneratedContractAddresses();

  return { contractsPath, sharedPath };
}

export function syncGeneratedContractAddresses() {
  const deployments = loadMergedDeployments(getContractsDeploymentsDir());
  const fileContent = buildGeneratedContractAddressesSource(deployments);
  ensureDir(path.dirname(SHARED_GENERATED_ADDRESSES_PATH));
  fs.writeFileSync(SHARED_GENERATED_ADDRESSES_PATH, fileContent);
}

export function syncDeploymentArtifacts() {
  ensureDir(getContractsDeploymentsDir());
  ensureDir(getSharedDeploymentsDir());

  for (const fileName of fs.readdirSync(getContractsDeploymentsDir())) {
    if (!fileName.endsWith(".json")) {
      continue;
    }

    const sourcePath = getContractsDeploymentPath(fileName);
    const targetPath = getSharedDeploymentPath(fileName);
    fs.copyFileSync(sourcePath, targetPath);
  }

  syncGeneratedContractAddresses();
}
