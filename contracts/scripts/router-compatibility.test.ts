import assert from "node:assert/strict";
import test from "node:test";
import { Interface, ZeroAddress, id } from "ethers";
import {
  ADD_LIQUIDITY_ETH_SIGNATURE,
  assertV2CompatibleRouter,
  hasV2AddLiquidityEthSelector,
  type RouterValidationProvider,
} from "./router-compatibility.js";

const ROUTER = "0x0000000000000000000000000000000000001000";
const WETH = "0x0000000000000000000000000000000000002000";
const FACTORY = "0x0000000000000000000000000000000000003000";
const SELECTOR = id(ADD_LIQUIDITY_ETH_SIGNATURE).slice(2, 10);
const METADATA_INTERFACE = new Interface([
  "function WETH() view returns (address)",
  "function factory() view returns (address)",
]);

type ProviderOptions = {
  code?: string;
  weth?: string;
  factory?: string;
  failCall?: "WETH" | "factory";
};

function createProvider(
  options: ProviderOptions = {}
): RouterValidationProvider {
  return {
    async getCode() {
      return options.code ?? `0x63${SELECTOR}6000`;
    },
    async call(transaction) {
      const request = METADATA_INTERFACE.parseTransaction({
        data: transaction.data,
      });
      if (!request) {
        throw new Error("unknown metadata call");
      }
      if (options.failCall === request.name) {
        throw new Error("metadata unavailable");
      }

      const configuredAddress =
        request.name === "WETH"
          ? options.weth ?? WETH
          : options.factory ?? FACTORY;
      return METADATA_INTERFACE.encodeFunctionResult(request.name, [
        configuredAddress,
      ]);
    },
  };
}

test("detects the exact V2 addLiquidityETH selector", () => {
  assert.equal(
    hasV2AddLiquidityEthSelector(`0x63${SELECTOR.toUpperCase()}6000`),
    true
  );
  assert.equal(hasV2AddLiquidityEthSelector("0x60006000"), false);
});

test("accepts a router with the required V2 surface", async () => {
  await assert.doesNotReject(async () => {
    assert.equal(
      await assertV2CompatibleRouter(createProvider(), ROUTER),
      ROUTER
    );
  });
});

test("rejects invalid and zero router addresses", async () => {
  await assert.rejects(
    assertV2CompatibleRouter(createProvider(), "invalid"),
    /not a valid address/
  );
  await assert.rejects(
    assertV2CompatibleRouter(createProvider(), ZeroAddress),
    /cannot be the zero address/
  );
});

test("rejects missing code or addLiquidityETH selector", async () => {
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ code: "0x" }), ROUTER),
    /does not expose V2-compatible/
  );
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ code: "0x60006000" }), ROUTER),
    /does not expose V2-compatible/
  );
});

test("rejects zero router metadata addresses", async () => {
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ weth: ZeroAddress }), ROUTER),
    /failed WETH\(\) validation: returned the zero address/
  );
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ factory: ZeroAddress }), ROUTER),
    /failed factory\(\) validation: returned the zero address/
  );
});

test("rejects unavailable router metadata", async () => {
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ failCall: "WETH" }), ROUTER),
    /failed WETH\(\) validation: metadata unavailable/
  );
  await assert.rejects(
    assertV2CompatibleRouter(createProvider({ failCall: "factory" }), ROUTER),
    /failed factory\(\) validation: metadata unavailable/
  );
});
