#!/usr/bin/env python3
"""Fail when a public snapshot contains private-release material.

The audit reports only file paths and rule names. It deliberately never prints
the matching line, because CI logs are public and a finding may itself be a
credential. External secret scanners remain required before each release.
"""

from __future__ import annotations

import hashlib
import ipaddress
import os
import re
import stat
import subprocess
import sys
import argparse
from pathlib import Path


DEFAULT_ROOT = Path(__file__).resolve().parents[1]

FORBIDDEN_PREFIXES = (
    ".git/",
    ".claude/",
    ".codex/",
    ".agents/",
    "docs/archive/",
    "docs/evidence/",
    "ops/",
    "scripts/deploy/",
)

FORBIDDEN_EXACT_PATHS = {
    ".git",
    ".env.production.template",
    ".gitmodules",
    ".github/workflows/ci.yml",
}

SECRET_PATTERNS = {
    "private-key-block": re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "github-token": re.compile(rb"(?:gh[opusr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})"),
    "aws-access-key": re.compile(rb"(?:AKIA|ASIA)[0-9A-Z]{16}"),
    "google-api-key": re.compile(rb"AIza[0-9A-Za-z_-]{35}"),
    "anthropic-key": re.compile(rb"sk-ant-[A-Za-z0-9_-]{20,}"),
    "openai-key": re.compile(rb"sk-(?!ant-)[A-Za-z0-9_-]{20,}"),
    "slack-token": re.compile(rb"xox[baprs]-[A-Za-z0-9-]{20,}"),
    "jwt-token": re.compile(rb"eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}"),
    "literal-bearer": re.compile(rb"Authorization\s*[:=]\s*Bearer\s+[A-Za-z0-9._~+/-]{16,}", re.I),
    "cloud-rpc-token": re.compile(rb"(?:infura\.io/v3|alchemy\.com/v2)/[A-Za-z0-9_-]{16,}", re.I),
    "tencent-operator-config": re.compile(rb"TENCENT_(?:HOST|USER|SSH|REPO)"),
    "private-key-literal": re.compile(
        rb"(?:private[_-]?key|mnemonic|seed)[^\r\n]{0,80}[:=]\s*['\"]?(?:0x)?[0-9a-f]{64}",
        re.I,
    ),
    "personal-macos-path": re.compile(rb"/Users/[A-Za-z0-9._-]+/"),
    "production-home-path": re.compile(rb"/home/(?:deploy|ubuntu|root)/"),
    "private-repository-reference": re.compile(
        rb"github\.com/corn12138/wallet-trade-console(?:/|\.git)",
        re.I,
    ),
}

FORBIDDEN_BINARY_SUFFIXES = {
    ".7z",
    ".db",
    ".gif",
    ".gz",
    ".jpeg",
    ".jpg",
    ".jks",
    ".key",
    ".p12",
    ".pdf",
    ".pem",
    ".pfx",
    ".png",
    ".rar",
    ".rdb",
    ".tar",
    ".webp",
    ".zip",
}

DOCUMENTATION_NETWORKS = tuple(
    ipaddress.ip_network(network)
    for network in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24")
)

TEXT_SKIP: set[str] = set()

FORBIDDEN_GENERATED_DIRECTORIES = {".next", "coverage", "dist", "node_modules", "out"}

# forge-std publishes shared, rate-limited RPC identifiers in StdChains. They
# are upstream test fixtures rather than maintainer credentials. The public env
# template uses an unmistakable placeholder and is checked separately below.
CLOUD_RPC_FIXTURE_PATHS = {
    "contracts-foundry/lib/forge-std/src/StdChains.sol",
    "contracts-foundry/lib/forge-std/test/StdChains.t.sol",
    "contracts-foundry/lib/openzeppelin-contracts/lib/forge-std/src/StdChains.sol",
    "contracts-foundry/lib/openzeppelin-contracts/lib/forge-std/test/StdChains.t.sol",
}

# The application bundles four reviewed font assets. Unknown binary content is
# rejected, and replacing a font requires an explicit checksum review.
ALLOWED_BINARY_SHA256 = {
    "apps/web/src/app/fonts/geist-mono.woff2": "b7ac144b394cbd81052d6397ec0c33397977b1d7e9bc095e744e652a378c6fb3",
    "apps/web/src/app/fonts/geist-sans.woff2": "1b5ebfb3a01a97343ac96873e6d59a8cb285c66012b6a1ac509cb2765e995ba8",
    "apps/web/src/app/fonts/inter-400.woff2": "27ae72daf88c7431896929273087c99910d019ae82dc0af7d86505c0f5ef5dbf",
    "apps/web/src/app/fonts/inter-600.woff2": "87d718a282da60f8ef79c2c85e2999bd0fe7a6ef3fc77ccb3ad8a5ff8474b1ef",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT, help="candidate or public checkout to scan")
    return parser.parse_args()


def filesystem_entries(root: Path) -> list[Path]:
    """Walk a non-Git candidate without following directory symlinks."""

    entries: list[Path] = []
    for directory, directory_names, file_names in os.walk(root, followlinks=False):
        current = Path(directory)
        for name in list(directory_names):
            path = current / name
            if path.is_symlink() or name == ".git" or name in FORBIDDEN_GENERATED_DIRECTORIES:
                entries.append(path)
                directory_names.remove(name)
        entries.extend(current / name for name in file_names)
    return sorted(entries)


def repository_files(root: Path) -> list[Path]:
    """Return every publishable file, including untracked pre-commit files.

    Git's exclude rules keep dependency/build output out of the scan, while
    ``--others`` closes the gap where a newly added file was invisible until it
    had already been committed. A generated candidate has no Git metadata, so
    it falls back to the same filesystem exclusions.
    """

    top_level = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--show-toplevel"],
        check=False,
        capture_output=True,
        text=True,
    )
    is_repository_root = (
        top_level.returncode == 0 and Path(top_level.stdout.strip()).resolve() == root
    )
    result = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        check=False,
        capture_output=True,
    ) if is_repository_root else None
    if result is not None and result.returncode == 0 and result.stdout:
        return sorted(
            root / item.decode()
            for item in result.stdout.split(b"\0")
            if item and ((root / item.decode()).exists() or (root / item.decode()).is_symlink())
        )

    return filesystem_entries(root)


def repository_gitlinks(root: Path) -> list[str]:
    """Return committed submodule entries, whose content is not scanned here."""

    top_level = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--show-toplevel"],
        check=False,
        capture_output=True,
        text=True,
    )
    if top_level.returncode != 0 or Path(top_level.stdout.strip()).resolve() != root:
        return []
    result = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--stage", "-z"],
        check=True,
        capture_output=True,
    )
    gitlinks: list[str] = []
    for entry in result.stdout.split(b"\0"):
        if not entry:
            continue
        metadata, path = entry.split(b"\t", 1)
        if metadata.split(b" ", 1)[0] == b"160000":
            gitlinks.append(path.decode())
    return gitlinks


def is_allowed_env(path: str) -> bool:
    name = Path(path).name
    if not name.startswith(".env"):
        return True
    return ".example" in name or ".template" in name


def has_public_ip(data: bytes) -> bool:
    for match in re.finditer(rb"(?<![0-9.])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?![0-9.])", data):
        try:
            address = ipaddress.ip_address(match.group().decode())
        except ValueError:
            continue
        allowed = address.is_loopback or address.is_unspecified or any(
            address in network for network in DOCUMENTATION_NETWORKS
        )
        if not allowed:
            return True
    return False


def check_workflow(relative: str, data: bytes, findings: set[tuple[str, str]]) -> None:
    if not relative.startswith(".github/workflows/"):
        return
    forbidden = re.compile(rb"secrets\.|pull_request_target|workflow_run|self-hosted|id-token:\s*write", re.I)
    if forbidden.search(data):
        findings.add((relative, "unsafe-public-workflow"))
    for action in re.finditer(rb"^\s*-?\s*uses:\s*[^\s@]+@([^\s#]+)", data, flags=re.MULTILINE):
        if not re.fullmatch(rb"[0-9a-f]{40}", action.group(1)):
            findings.add((relative, "mutable-action-reference"))
    if b"permissions:\n  contents: read" not in data:
        findings.add((relative, "workflow-permissions-not-read-only"))


def main(root: Path) -> int:
    root = root.expanduser().resolve()
    if not root.is_dir() or root == Path(root.anchor):
        raise ValueError(f"audit root is not a safe directory: {root}")
    findings: set[tuple[str, str]] = set()

    for relative in repository_gitlinks(root):
        findings.add((relative, "git-submodule"))

    files = repository_files(root)
    for file_path in files:
        relative = file_path.relative_to(root).as_posix()
        if any(part in FORBIDDEN_GENERATED_DIRECTORIES for part in Path(relative).parts):
            findings.add((relative, "generated-directory"))
        if relative in FORBIDDEN_EXACT_PATHS or relative.startswith(FORBIDDEN_PREFIXES):
            findings.add((relative, "forbidden-path"))

        mode = file_path.lstat().st_mode
        if stat.S_ISLNK(mode):
            findings.add((relative, "symlink"))
            continue
        if stat.S_ISDIR(mode):
            continue
        if not stat.S_ISREG(mode):
            findings.add((relative, "special-file"))
            continue
        if not is_allowed_env(relative):
            findings.add((relative, "filled-env-file"))
        if file_path.suffix.lower() in FORBIDDEN_BINARY_SUFFIXES:
            findings.add((relative, "binary-or-secret-file"))
        if relative in TEXT_SKIP:
            continue
        if file_path.stat().st_size > 5_000_000:
            findings.add((relative, "oversized-file"))
            continue

        data = file_path.read_bytes()
        if b"\0" in data:
            expected_hash = ALLOWED_BINARY_SHA256.get(relative)
            if expected_hash is None:
                findings.add((relative, "binary-content"))
            elif hashlib.sha256(data).hexdigest() != expected_hash:
                findings.add((relative, "binary-hash-mismatch"))
            continue
        if relative == "services/api-go/go.mod" and not re.search(
            rb"^module github\.com/corn12138/wallet-trade-console-public/services/api-go$",
            data,
            flags=re.MULTILINE,
        ):
            findings.add((relative, "unexpected-public-go-module"))
        for rule, pattern in SECRET_PATTERNS.items():
            if rule == "cloud-rpc-token" and relative in CLOUD_RPC_FIXTURE_PATHS:
                continue
            if (
                rule == "cloud-rpc-token"
                and relative == "apps/web/.env.vercel.example"
                and b"replace-with-your-key" in data
            ):
                continue
            if pattern.search(data):
                findings.add((relative, rule))
        if has_public_ip(data):
            findings.add((relative, "public-ipv4-address"))
        check_workflow(relative, data, findings)

    if findings:
        for relative, rule in sorted(findings):
            print(f"public-release audit: {rule}: {relative}", file=sys.stderr)
        return 1

    print(f"public-release audit: PASS ({len(files)} files checked)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(parse_args().root))
