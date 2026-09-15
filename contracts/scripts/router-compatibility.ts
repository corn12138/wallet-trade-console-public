import { Interface, ZeroAddress, getAddress, id } from "ethers";

export const ADD_LIQUIDITY_ETH_SIGNATURE =
  "addLiquidityETH(address,uint256,uint256,uint256,address,uint256)";

const ROUTER_SELECTOR = id(ADD_LIQUIDITY_ETH_SIGNATURE)
  .slice(2, 10)
  .toLowerCase();
const ROUTER_VIEW_INTERFACE = new Interface([
  "function WETH() view returns (address)",
  "function factory() view returns (address)",
]);

export type RouterValidationProvider = {
  getCode(address: string): Promise<string>;
  call(transaction: { to: string; data: string }): Promise<string>;
};

export function hasV2AddLiquidityEthSelector(bytecode: string): boolean {
  return bytecode.toLowerCase().includes(ROUTER_SELECTOR);
}

export async function assertV2CompatibleRouter(
  provider: RouterValidationProvider,
  routerAddress: string,
  label = "launchpad DEX router"
): Promise<string> {
  let router: string;
  try {
    router = getAddress(routerAddress);
  } catch {
    throw new Error(`${label} is not a valid address: ${routerAddress}`);
  }
  if (router === ZeroAddress) {
    throw new Error(`${label} cannot be the zero address`);
  }

  const bytecode = await provider.getCode(router);
  // The local project Router exposes ERC20 liquidity only; checking the exact selector prevents
  // silently configuring it as the ETH-capable V2 router required by BondingCurve graduation.
  if (bytecode === "0x" || !hasV2AddLiquidityEthSelector(bytecode)) {
    throw new Error(
      `${label} at ${router} does not expose V2-compatible ${ADD_LIQUIDITY_ETH_SIGNATURE}`
    );
  }

  for (const functionName of ["WETH", "factory"] as const) {
    try {
      const data = ROUTER_VIEW_INTERFACE.encodeFunctionData(functionName);
      const result = await provider.call({ to: router, data });
      const [configuredAddress] = ROUTER_VIEW_INTERFACE.decodeFunctionResult(
        functionName,
        result
      );
      if (configuredAddress === ZeroAddress) {
        throw new Error("returned the zero address");
      }
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      throw new Error(
        `${label} at ${router} failed ${functionName}() validation: ${detail}`
      );
    }
  }

  return router;
}
