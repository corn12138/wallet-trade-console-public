#!/usr/bin/env python3
"""Generate a sanitized public candidate from a committed private revision.

The command only writes to a new, empty output directory. It never changes a
Git remote, commits, or pushes, so a reviewer can inspect and test the complete
candidate before any public object exists.
"""

from __future__ import annotations

import argparse
import re
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path


PUBLIC_ROOT = Path(__file__).resolve().parents[1]
PUBLIC_GO_MODULE = "github.com/corn12138/wallet-trade-console-public/services/api-go"

SOURCE_PATHS = (
    ".dockerignore",
    ".gitignore",
    "LICENSE",
    "package.json",
    "pnpm-lock.yaml",
    "pnpm-workspace.yaml",
    "tsconfig.base.json",
    "apps/web",
    "contracts",
    "contracts-foundry",
    "packages",
    "services/api-go",
)

# These files intentionally differ from the private integration tree. They
# remove environment-specific behavior or document the public trust boundary.
PUBLIC_OVERLAYS = (
    ".github",
    ".gitleaks.toml",
    ".trufflehog-exclude-paths.txt",
    "CONTRIBUTING.md",
    "README.md",
    "SECURITY.md",
    "docs",
    "scripts",
    "pnpm-workspace.yaml",
    "apps/web/.env.vercel.example",
    "apps/web/next.config.js",
    "apps/web/src/lib/api/base-url.spec.ts",
    "apps/web/src/lib/api/base-url.ts",
    "contracts/.env.example",
    "contracts-foundry/README.md",
    "contracts-foundry/script/Deploy.s.sol",
    "packages/database/package.json",
    "services/api-go/.env.example",
    "services/api-go/README.md",
)

EXCLUDED_PATHS = (
    "apps/web/atlas_web3_pc_technical_architecture_zh.md",
    "apps/web/stitch_mobile_ui_prompt_pack_2026.md",
    "apps/web/src/lib/web3/contract-addresses.generated.ts",
    "contracts-foundry/broadcast",
    "contracts-foundry/lib/openzeppelin-contracts/audits",
    "contracts-foundry/lib/openzeppelin-contracts/certora/reports",
    "contracts-foundry/script/inspect-contracts.sh",
    "packages/database/prisma/migrations",
    "packages/database/prisma/schema-additions.prisma",
    "packages/database/prisma/schema-enhanced.prisma",
    "packages/database/prisma/schema-migration-step1.prisma",
    "packages/database/prisma/seed.ts",
    "services/api-go/.env.remote.local.example",
)

NORMALIZED_TEXT_PATHS = (
    "apps/web/public/apple-touch-icon.svg",
    "apps/web/public/og-image.svg",
    "contracts-foundry/src/launchpad/LaunchToken.sol",
    "contracts/src/launchpad/LaunchToken.sol",
    "packages/database/prisma/schema.prisma",
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="private Git checkout")
    parser.add_argument("--ref", default="origin/main", help="committed Git revision")
    parser.add_argument("--output", required=True, type=Path, help="new or empty candidate directory")
    return parser.parse_args()


def run(*args: str, cwd: Path | None = None, capture: bool = False) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        args,
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def validate_output(output: Path) -> Path:
    resolved = output.expanduser().resolve()
    if resolved == Path(resolved.anchor):
        raise ValueError("output must not be a filesystem root")
    if resolved.exists() and any(resolved.iterdir()):
        raise ValueError(f"output must be empty: {resolved}")
    resolved.mkdir(parents=True, exist_ok=True)
    return resolved


def extract_revision(source: Path, ref: str, destination: Path) -> str:
    source = source.expanduser().resolve()
    if not (source / ".git").exists():
        raise ValueError(f"source is not a Git checkout: {source}")

    resolved_ref = run("git", "rev-parse", "--verify", f"{ref}^{{commit}}", cwd=source, capture=True)
    revision = resolved_ref.stdout.decode().strip()

    archive_path = destination.parent / "source.tar"
    with archive_path.open("wb") as archive_file:
        subprocess.run(
            ["git", "archive", "--format=tar", ref],
            cwd=source,
            check=True,
            stdout=archive_file,
        )

    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive_path) as archive:
        destination_root = destination.resolve()
        for member in archive.getmembers():
            member_path = (destination / member.name).resolve()
            if destination_root not in member_path.parents and member_path != destination_root:
                raise ValueError(f"unsafe archive path: {member.name}")
        try:
            archive.extractall(destination, filter="data")
        except TypeError:
            # Python versions before extraction filters are still protected by
            # the explicit path-containment check above.
            archive.extractall(destination)
    return revision


def copy_path(source_root: Path, destination_root: Path, relative: str) -> None:
    source = source_root / relative
    destination = destination_root / relative
    if not source.exists():
        raise FileNotFoundError(f"required export path is missing: {relative}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    if source.is_dir():
        shutil.copytree(source, destination, dirs_exist_ok=True, symlinks=True)
    else:
        shutil.copy2(source, destination)


def remove_excluded(candidate: Path) -> None:
    for relative in EXCLUDED_PATHS:
        target = candidate / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists() or target.is_symlink():
            target.unlink()


def prune_vendor_dependencies(candidate: Path) -> None:
    """Keep only the vendored Solidity sources and their license files."""

    keep_by_root = {
        candidate / "contracts-foundry/lib/forge-std": {"src", "LICENSE-APACHE", "LICENSE-MIT"},
        candidate / "contracts-foundry/lib/openzeppelin-contracts": {"contracts", "LICENSE"},
    }
    for root, keep in keep_by_root.items():
        for child in root.iterdir():
            if child.name in keep:
                continue
            if child.is_dir():
                shutil.rmtree(child)
            else:
                child.unlink()


def apply_public_overlays(candidate: Path) -> None:
    for relative in PUBLIC_OVERLAYS:
        target = candidate / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists() or target.is_symlink():
            target.unlink()
        copy_path(PUBLIC_ROOT, candidate, relative)


def rewrite_go_module(candidate: Path) -> None:
    go_mod = candidate / "services/api-go/go.mod"
    match = re.search(r"^module\s+(\S+)$", go_mod.read_text(), flags=re.MULTILINE)
    if not match:
        raise ValueError("services/api-go/go.mod has no module declaration")
    private_module = match.group(1)

    for path in (candidate / "services/api-go").rglob("*"):
        if not path.is_file() or path.stat().st_size > 5_000_000:
            continue
        data = path.read_bytes()
        if b"\0" in data:
            continue
        text = data.decode("utf-8")
        if private_module in text:
            path.write_text(text.replace(private_module, PUBLIC_GO_MODULE))


def scrub_generic_test_examples(candidate: Path) -> None:
    path = candidate / "services/api-go/internal/discover/discover_realdb_test.go"
    text = path.read_text()
    text = re.sub(
        r"(WALLET_TRADE_TEST_DATABASE_URL='postgres://[^'\s]+/)[^?'\s]+",
        r"\1wallet_trade_test",
        text,
    )
    path.write_text(text)


def normalize_trailing_whitespace(candidate: Path) -> None:
    """Keep the exported root commit free of inherited whitespace errors."""

    for relative in NORMALIZED_TEXT_PATHS:
        path = candidate / relative
        lines = path.read_text().splitlines(keepends=True)
        normalized = []
        for line in lines:
            body = line.rstrip("\r\n")
            ending = line[len(body) :]
            normalized.append(body.rstrip(" \t") + ending)
        path.write_text("".join(normalized))


def main() -> int:
    args = parse_args()
    output = validate_output(args.output)

    with tempfile.TemporaryDirectory(prefix="wallet-public-export-") as temporary:
        raw = Path(temporary) / "raw"
        revision = extract_revision(args.source, args.ref, raw)
        for relative in SOURCE_PATHS:
            copy_path(raw, output, relative)

    remove_excluded(output)
    prune_vendor_dependencies(output)
    apply_public_overlays(output)
    rewrite_go_module(output)
    scrub_generic_test_examples(output)
    normalize_trailing_whitespace(output)

    # The local policy is the first gate. gitleaks, trufflehog, dependency
    # checks, builds, and human review still run before a public push.
    run("python3", "scripts/public_release_audit.py", cwd=output)
    print(f"public candidate generated from committed revision {revision}")
    print(f"review and test before publishing: {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
