#!/usr/bin/env python3
"""Validate the execution interface manifest and the /trade declarations, and
generate the Go embedded copy of the manifest.

Sources:
  packages/shared/contracts/execution-interfaces.json
  packages/shared/execution-registry/declarations.json
Generated:
  services/api-go/internal/executionregistry/interfaces.generated.json
The Web imports the manifest JSON directly. Selectors are derived by each
consumer (Go abi.Selector, viem toFunctionSelector); a parity test in each
language pins them.

Usage: generate-execution-registry-contracts.py [--check] [--self-test]
"""
import argparse
import copy
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / "packages/shared/contracts/execution-interfaces.json"
DECLARATIONS = ROOT / "packages/shared/execution-registry/declarations.json"
TARGET_GO = ROOT / "services/api-go/internal/executionregistry/interfaces.generated.json"

INTERFACE_ID = re.compile(r"^[a-z][a-z0-9]*(\.[a-zA-Z][a-zA-Z0-9]*)+\.v[1-9][0-9]*$")
SIGNATURE = re.compile(r"^[a-zA-Z_][a-zA-Z0-9_]*\(((address|bool|uint256)(,(address|bool|uint256))*)?\)$")
ROLE = re.compile(r"^[a-z][a-zA-Z0-9]{0,31}$")
NAME = re.compile(r"^[A-Za-z][A-Za-z0-9]{0,63}$")
MARKET = re.compile(r"^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$")
CHAIN = re.compile(r"^[1-9][0-9]{0,9}$")
OPERATIONS = {"trade.approveCollateral", "trade.openPosition", "trade.closePosition"}
OPERATION_INTERFACE = {"trade.approveCollateral": "erc20.approve.v1", "trade.openPosition": "perp.positionManager.open.v1",
                       "trade.closePosition": "perp.positionManager.close.v1"}


def keys(value, allowed, where, required=None):
    if not isinstance(value, dict):
        raise ValueError(f"{where}: object required")
    extra = set(value) - set(allowed)
    if extra:
        raise ValueError(f"{where}: unknown keys {sorted(extra)}")
    missing = set(required if required is not None else allowed) - set(value)
    if missing:
        raise ValueError(f"{where}: missing keys {sorted(missing)}")


def validate_manifest(manifest):
    keys(manifest, {"schemaVersion", "description", "interfaces"}, "manifest", {"schemaVersion", "interfaces"})
    if manifest["schemaVersion"] != 1:
        raise ValueError("manifest: unsupported schemaVersion")
    if not manifest["interfaces"]:
        raise ValueError("manifest: no interfaces")
    for interface_id, item in manifest["interfaces"].items():
        if not INTERFACE_ID.match(interface_id):
            raise ValueError(f"manifest: bad interface id {interface_id!r}")
        keys(item, {"signature", "argRoles"}, interface_id)
        if not SIGNATURE.match(item["signature"]):
            raise ValueError(f"{interface_id}: unsupported signature {item['signature']!r}")
        params = item["signature"][item["signature"].index("(") + 1:-1]
        count = len(params.split(",")) if params else 0
        roles = item["argRoles"]
        if not isinstance(roles, list) or len(roles) != count or len(set(roles)) != len(roles) or not all(isinstance(r, str) and ROLE.match(r) for r in roles):
            raise ValueError(f"{interface_id}: argRoles must name each parameter once")
    for operation, interface_id in OPERATION_INTERFACE.items():
        if interface_id not in manifest["interfaces"]:
            raise ValueError(f"manifest: operation {operation} needs {interface_id}")


def validate_declarations(declarations, manifest):
    keys(declarations, {"schemaVersion", "description", "chains"}, "declarations", {"schemaVersion", "chains"})
    if declarations["schemaVersion"] != 1:
        raise ValueError("declarations: unsupported schemaVersion")
    for chain_id, chain in declarations["chains"].items():
        if not CHAIN.match(chain_id):
            raise ValueError(f"declarations: bad chain id {chain_id!r}")
        keys(chain, {"entries", "markets", "operations"}, chain_id)
        for name, entry in chain["entries"].items():
            if not NAME.match(name):
                raise ValueError(f"{chain_id}: bad entry name {name!r}")
            keys(entry, {"interfaces"}, f"{chain_id}.{name}")
            interfaces = entry["interfaces"]
            if interfaces != sorted(set(interfaces)) or any(i not in manifest["interfaces"] for i in interfaces):
                raise ValueError(f"{chain_id}.{name}: interfaces must be sorted, unique and in the manifest")
        for symbol, market in chain["markets"].items():
            if not MARKET.match(symbol):
                raise ValueError(f"{chain_id}: bad market {symbol!r}")
            keys(market, {"positionManager", "indexToken", "collateralToken", "indexDecimals", "collateralDecimals", "pricePrecision", "components"}, f"{chain_id}.{symbol}")
            for role in ("positionManager", "indexToken", "collateralToken"):
                if market[role] not in chain["entries"]:
                    raise ValueError(f"{chain_id}.{symbol}: {role} {market[role]!r} is not a declared entry")
            for field, top in (("indexDecimals", 36), ("collateralDecimals", 30), ("pricePrecision", 36)):
                if not isinstance(market[field], int) or isinstance(market[field], bool) or not 0 <= market[field] <= top:
                    raise ValueError(f"{chain_id}.{symbol}: {field} out of range")
            if market["pricePrecision"] != 30:
                raise ValueError(f"{chain_id}.{symbol}: the trade encoder supports pricePrecision 30 only")
            components = market["components"]
            if components != sorted(set(components)) or any(c not in chain["entries"] for c in components):
                raise ValueError(f"{chain_id}.{symbol}: components must be sorted, unique, declared entries")
            pm = chain["entries"][market["positionManager"]]["interfaces"]
            collateral = chain["entries"][market["collateralToken"]]["interfaces"]
            for operation, item in chain["operations"].items():
                needs = OPERATION_INTERFACE[operation]
                holder = collateral if operation == "trade.approveCollateral" else pm
                if needs not in holder:
                    raise ValueError(f"{chain_id}.{symbol}: {operation} needs {needs} on its target")
        if set(chain["operations"]) - OPERATIONS:
            raise ValueError(f"{chain_id}: unknown operations")
        for operation, item in chain["operations"].items():
            keys(item, {"status"}, f"{chain_id}.{operation}")
            if item["status"] not in ("enabled", "disabled"):
                raise ValueError(f"{chain_id}.{operation}: bad status")
            # An exit cannot be disabled while its market exists (§4.2, §7 E8).
            if operation == "trade.closePosition" and item["status"] != "enabled" and chain["markets"]:
                raise ValueError(f"{chain_id}: trade.closePosition cannot be disabled while markets exist")
        if chain["markets"] and "trade.closePosition" not in chain["operations"]:
            raise ValueError(f"{chain_id}: trade.closePosition must be declared while markets exist")


def generated(manifest):
    return json.dumps(manifest, indent=2, sort_keys=True) + "\n"


def self_test():
    manifest = json.loads(MANIFEST.read_text())
    declarations = json.loads(DECLARATIONS.read_text())
    validate_manifest(manifest)
    validate_declarations(declarations, manifest)
    chain = next(iter(declarations["chains"]))
    bad = []
    def mutate(label, target, change):
        value = copy.deepcopy(manifest if target == "m" else declarations)
        change(value)
        try:
            validate_manifest(value) if target == "m" else validate_declarations(value, manifest)
        except ValueError:
            return
        bad.append(label)
    mutate("unknown manifest key", "m", lambda v: v.update(extra=1))
    mutate("role count mismatch", "m", lambda v: v["interfaces"]["erc20.approve.v1"]["argRoles"].pop())
    mutate("tuple signature", "m", lambda v: v["interfaces"]["erc20.approve.v1"].update(signature="approve((address,uint256))"))
    mutate("disabled exit", "d", lambda v: v["chains"][chain]["operations"]["trade.closePosition"].update(status="disabled"))
    mutate("undeclared market token", "d", lambda v: v["chains"][chain]["markets"]["ETH-USD"].update(indexToken="Nope"))
    mutate("unknown interface", "d", lambda v: v["chains"][chain]["entries"]["MockUSDC"].update(interfaces=["erc20.approve.v9"]))
    mutate("target lacks interface", "d", lambda v: v["chains"][chain]["entries"]["MockUSDC"].update(interfaces=[]))
    mutate("precision other than 30", "d", lambda v: v["chains"][chain]["markets"]["ETH-USD"].update(pricePrecision=18))
    mutate("unsorted components", "d", lambda v: v["chains"][chain]["markets"]["ETH-USD"].update(components=["PerpVault", "PerpMarket"]))
    if bad:
        raise SystemExit("self-test accepted invalid input: " + ", ".join(bad))
    print("self-test: 9 invalid inputs refused")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
    manifest = json.loads(MANIFEST.read_text())
    validate_manifest(manifest)
    validate_declarations(json.loads(DECLARATIONS.read_text()), manifest)
    value = generated(manifest)
    if args.check:
        if not TARGET_GO.exists() or TARGET_GO.read_text() != value:
            raise SystemExit("execution interface manifest copy is stale")
    else:
        TARGET_GO.parent.mkdir(parents=True, exist_ok=True)
        TARGET_GO.write_text(value)
    print(f"execution registry contracts: {len(manifest['interfaces'])} interfaces, declarations valid")


if __name__ == "__main__":
    main()
