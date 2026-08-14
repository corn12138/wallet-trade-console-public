#!/usr/bin/env python3
"""Fail when a public snapshot contains private-release material.

The audit reports only file paths and rule names. It deliberately never prints
the matching line, because CI logs are public and a finding may itself be a
credential. External secret scanners remain required before each release.
"""

from __future__ import annotations

import ipaddress
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SELF = Path(__file__).resolve()

FORBIDDEN_PREFIXES = (
    ".claude/",
    ".codex/",
    ".agents/",
    "docs/archive/",
    "docs/evidence/",
    "ops/",
    "scripts/deploy/",
)

FORBIDDEN_EXACT_PATHS = {
    ".env.production.template",
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

TEXT_SKIP = {
    SELF.relative_to(ROOT).as_posix(),
}

# forge-std publishes shared, rate-limited RPC identifiers in StdChains. They
# are upstream test fixtures rather than maintainer credentials. The public env
# template uses an unmistakable placeholder and is checked separately below.
CLOUD_RPC_FIXTURE_PATHS = {
    "contracts-foundry/lib/forge-std/src/StdChains.sol",
    "contracts-foundry/lib/forge-std/test/StdChains.t.sol",
    "contracts-foundry/lib/openzeppelin-contracts/lib/forge-std/src/StdChains.sol",
    "contracts-foundry/lib/openzeppelin-contracts/lib/forge-std/test/StdChains.t.sol",
}


def repository_files() -> list[Path]:
    """Use tracked files in Git and the filesystem before the first commit."""

    result = subprocess.run(
        ["git", "-C", str(ROOT), "ls-files", "-z"],
        check=False,
        capture_output=True,
    )
    if result.returncode == 0 and result.stdout:
        return [ROOT / item.decode() for item in result.stdout.split(b"\0") if item]

    ignored = {".git", "node_modules", ".next", "out", "dist", "coverage"}
    return [
        path
        for path in ROOT.rglob("*")
        if path.is_file() and not any(part in ignored for part in path.relative_to(ROOT).parts)
    ]


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


def main() -> int:
    findings: set[tuple[str, str]] = set()

    for file_path in repository_files():
        relative = file_path.relative_to(ROOT).as_posix()
        if relative in FORBIDDEN_EXACT_PATHS or relative.startswith(FORBIDDEN_PREFIXES):
            findings.add((relative, "forbidden-path"))
        if not is_allowed_env(relative):
            findings.add((relative, "filled-env-file"))
        if file_path.suffix.lower() in FORBIDDEN_BINARY_SUFFIXES:
            findings.add((relative, "binary-or-secret-file"))
        if relative in TEXT_SKIP or file_path.stat().st_size > 5_000_000:
            continue

        data = file_path.read_bytes()
        if b"\0" in data:
            continue
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

    print(f"public-release audit: PASS ({len(repository_files())} files checked)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
