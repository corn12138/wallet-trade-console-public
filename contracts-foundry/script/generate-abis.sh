#!/usr/bin/env bash
# Generate TypeScript ABI files from Foundry build artifacts.
#
# Source of truth: contracts-foundry/src/**/*.sol
# Output:          packages/shared/src/web3/abis/<Name>.ts
#
# Each output file is:
#   export const <Name>ABI = [...] as const;
#
# Re-runnable. Requires forge (>=1.5) and jq.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FOUNDRY_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$FOUNDRY_ROOT/.." && pwd)"
OUT_DIR="$REPO_ROOT/packages/shared/src/web3/abis"

# Contracts to export. Order is alphabetical for stable diffs.
CONTRACTS=(
  BondingCurve
  BridgeGateway
  IpfsArtworkNFT
  LaunchToken
  OnchainArtworkNFT
  PerpMarket
  PerpOracle
  PerpVault
  PositionManager
  Router
  StakingPool
  TokenFactory
  TradingPair
  TradingPairFactory
)

cd "$FOUNDRY_ROOT"

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required but not on PATH" >&2
  exit 1
fi

if ! command -v forge >/dev/null 2>&1; then
  echo "error: forge is required but not on PATH" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"

echo "Building Foundry artifacts…"
forge build --silent

echo "Generating ABIs into $OUT_DIR"

written=0
for name in "${CONTRACTS[@]}"; do
  raw_abi="$(forge inspect "$name" abi --json)"
  # Pretty-print with 2-space indent, sort keys for byte-stable output.
  pretty="$(printf '%s' "$raw_abi" | jq -S --indent 2 '.')"
  out_file="$OUT_DIR/${name}.ts"
  {
    echo "// AUTO-GENERATED — do not edit by hand."
    echo "// Source: contracts-foundry/src — regenerate via contracts-foundry/script/generate-abis.sh"
    echo ""
    echo "export const ${name}ABI = ${pretty} as const;"
  } > "$out_file"
  echo "  wrote $out_file"
  written=$((written + 1))
done

echo ""
echo "Done. Generated ${written} ABI files."
