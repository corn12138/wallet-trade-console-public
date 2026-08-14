import { network } from "hardhat";

export async function getHardhatConnection(networkName?: string) {
  return networkName ? network.connect(networkName) : network.connect();
}

export async function getHardhatEthers(networkName?: string) {
  const connection = await getHardhatConnection(networkName);
  return connection.ethers;
}

export async function loadHardhatFixture<T>(
  fixture: (connection: Awaited<ReturnType<typeof getHardhatConnection>>) => Promise<T>,
  networkName?: string
) {
  const connection = await getHardhatConnection(networkName);
  return connection.networkHelpers.loadFixture(fixture);
}
