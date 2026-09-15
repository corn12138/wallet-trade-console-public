#!/usr/bin/env python3
"""Enforce the transitional Foundry-canonical Solidity source boundary."""

from __future__ import annotations

import argparse
import shutil
import tempfile
from dataclasses import dataclass
from pathlib import Path


CANONICAL_ROOT = Path("contracts-foundry/src")
COMPATIBILITY_ROOT = Path("contracts/src")

# These directories intentionally exist on only one side during the migration.
FOUNDRY_ONLY_PREFIXES = ("bridge",)


@dataclass(frozen=True)
class Finding:
    summary: str
    path: str
    remediation: str


def _solidity_sources(root: Path) -> dict[str, Path]:
    return {
        path.relative_to(root).as_posix(): path for path in root.rglob("*.sol") if path.is_file()
    }


def _under_prefix(path: str, prefixes: tuple[str, ...]) -> bool:
    return any(path == prefix or path.startswith(f"{prefix}/") for prefix in prefixes)


def audit(repo_root: Path) -> list[Finding]:
    canonical_dir = repo_root / CANONICAL_ROOT
    compatibility_dir = repo_root / COMPATIBILITY_ROOT
    missing_roots = [path for path in (canonical_dir, compatibility_dir) if not path.is_dir()]
    if missing_roots:
        return [
            Finding(
                "source root is missing",
                path.relative_to(repo_root).as_posix(),
                "run this guard from a complete wallet-trade-console checkout",
            )
            for path in missing_roots
        ]

    canonical = _solidity_sources(canonical_dir)
    compatibility = _solidity_sources(compatibility_dir)
    findings: list[Finding] = []

    for relative_path in sorted(canonical.keys() - compatibility.keys()):
        if _under_prefix(relative_path, FOUNDRY_ONLY_PREFIXES):
            continue
        findings.append(
            Finding(
                "canonical source has no Hardhat compatibility mirror",
                relative_path,
                f"copy {CANONICAL_ROOT / relative_path} to "
                f"{COMPATIBILITY_ROOT / relative_path}, preserving the relative path",
            )
        )

    for relative_path in sorted(compatibility.keys() - canonical.keys()):
        findings.append(
            Finding(
                "Hardhat-only source has no Foundry canonical source",
                relative_path,
                f"make {CANONICAL_ROOT / relative_path} canonical, then mirror it at "
                f"{COMPATIBILITY_ROOT / relative_path}, or delete it if it is obsolete",
            )
        )

    for relative_path in sorted(canonical.keys() & compatibility.keys()):
        if canonical[relative_path].read_bytes() == compatibility[relative_path].read_bytes():
            continue
        findings.append(
            Finding(
                "Hardhat compatibility mirror differs from the Foundry canonical source",
                relative_path,
                f"keep {CANONICAL_ROOT / relative_path} as the source of truth and refresh "
                f"{COMPATIBILITY_ROOT / relative_path} byte-for-byte",
            )
        )

    return findings


def sync_compatibility_mirror(repo_root: Path) -> list[str]:
    canonical_dir = repo_root / CANONICAL_ROOT
    compatibility_dir = repo_root / COMPATIBILITY_ROOT
    if not canonical_dir.is_dir() or not compatibility_dir.is_dir():
        raise RuntimeError("both Solidity source roots must already exist")

    compatibility_root = compatibility_dir.resolve()
    changed: list[str] = []
    for relative_path, source in sorted(_solidity_sources(canonical_dir).items()):
        if _under_prefix(relative_path, FOUNDRY_ONLY_PREFIXES):
            continue

        target = compatibility_dir / relative_path
        # Refuse links so the one-way sync cannot write outside the compatibility tree.
        if target.is_symlink() or not target.resolve().is_relative_to(compatibility_root):
            raise RuntimeError(f"unsafe compatibility target: {target}")

        canonical_bytes = source.read_bytes()
        if target.is_file() and target.read_bytes() == canonical_bytes:
            continue
        if target.exists() and not target.is_file():
            raise RuntimeError(f"compatibility target is not a regular file: {target}")

        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
        changed.append(relative_path)

    return changed


def _write(root: Path, relative_path: str, content: str) -> None:
    target = root / relative_path
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")


def run_self_test() -> None:
    cases = (
        ("matching sources and Foundry-only exception", (), ()),
        ("content drift", (("core/Pair.sol", "contract Drift {}"),), ("mirror differs",)),
        (
            "missing compatibility mirror",
            (("launchpad/New.sol", None),),
            ("no Hardhat compatibility",),
        ),
        (
            "unexpected Hardhat-only source",
            (("mocks/RouterMock.sol", "hardhat-only"),),
            ("no Foundry canonical",),
        ),
    )

    for name, mutations, expected_fragments in cases:
        with tempfile.TemporaryDirectory(prefix="contract-source-parity-") as temp_dir:
            repo_root = Path(temp_dir)
            shared_source = "// SPDX-License-Identifier: MIT\ncontract Pair {}\n"
            _write(repo_root, "contracts-foundry/src/core/Pair.sol", shared_source)
            _write(repo_root, "contracts/src/core/Pair.sol", shared_source)
            _write(repo_root, "contracts-foundry/src/bridge/Gateway.sol", "contract Gateway {}\n")

            for relative_path, mode in mutations:
                if mode is None:
                    _write(repo_root, f"contracts-foundry/src/{relative_path}", "contract New {}\n")
                elif mode == "hardhat-only":
                    _write(repo_root, f"contracts/src/{relative_path}", "contract Old {}\n")
                else:
                    _write(repo_root, f"contracts/src/{relative_path}", mode)

            rendered = "\n".join(
                f"{finding.summary}: {finding.path}" for finding in audit(repo_root)
            )
            if not expected_fragments and rendered:
                raise AssertionError(f"{name}: expected success, got {rendered}")
            for fragment in expected_fragments:
                if fragment not in rendered:
                    raise AssertionError(f"{name}: missing {fragment!r} in {rendered!r}")

    with tempfile.TemporaryDirectory(prefix="contract-source-sync-") as temp_dir:
        repo_root = Path(temp_dir)
        canonical_pair = "contract Pair { uint256 public version = 2; }\n"
        _write(repo_root, "contracts-foundry/src/core/Pair.sol", canonical_pair)
        _write(repo_root, "contracts-foundry/src/core/New.sol", "contract New {}\n")
        _write(repo_root, "contracts-foundry/src/bridge/Gateway.sol", "contract Gateway {}\n")
        _write(repo_root, "contracts/src/core/Pair.sol", "contract Pair {}\n")

        changed = sync_compatibility_mirror(repo_root)
        if changed != ["core/New.sol", "core/Pair.sol"]:
            raise AssertionError(f"sync changed unexpected paths: {changed}")
        if (repo_root / "contracts/src/core/Pair.sol").read_text() != canonical_pair:
            raise AssertionError("sync did not refresh the drifted compatibility mirror")
        if (repo_root / "contracts/src/bridge/Gateway.sol").exists():
            raise AssertionError("sync copied the Foundry-only bridge exception")
        if audit(repo_root):
            raise AssertionError("sync did not restore source parity")

        extra = repo_root / "contracts/src/legacy/Old.sol"
        _write(repo_root, "contracts/src/legacy/Old.sol", "contract Old {}\n")
        sync_compatibility_mirror(repo_root)
        if not extra.exists() or not audit(repo_root):
            raise AssertionError("sync must preserve and report unexpected Hardhat-only sources")

    print("contract-source-parity self-test: PASS")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--self-test", action="store_true", help="run isolated guard fixtures")
    mode.add_argument(
        "--sync",
        action="store_true",
        help="refresh the Hardhat mirror from Foundry without deleting Hardhat-only files",
    )
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parents[2],
        help="repository root (defaults to the checkout containing this script)",
    )
    args = parser.parse_args()

    if args.self_test:
        run_self_test()
        return 0

    repo_root = args.repo_root.resolve()
    if args.sync:
        try:
            changed = sync_compatibility_mirror(repo_root)
        except RuntimeError as error:
            print(f"contract-source-parity sync: FAIL: {error}")
            return 1
        print(f"contract-source-parity sync: refreshed {len(changed)} file(s)")

    findings = audit(repo_root)
    if findings:
        print("contract-source-parity: FAIL")
        for finding in findings:
            print(f"  - {finding.summary}: {finding.path}")
            print(f"    fix: {finding.remediation}")
        return 1

    print(
        "contract-source-parity: PASS "
        f"({CANONICAL_ROOT} canonical; {COMPATIBILITY_ROOT} compatibility mirror)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
