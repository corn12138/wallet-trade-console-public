#!/usr/bin/env python3
"""Generate a sanitized public candidate from a committed private revision.

The command only writes to a new, empty output directory. It never changes a
Git remote, commits, or pushes, so a reviewer can inspect and test the complete
candidate before any public object exists.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
from pathlib import Path, PurePosixPath


PUBLIC_ROOT = Path(__file__).resolve().parents[1]
PUBLIC_GO_MODULE = "github.com/corn12138/wallet-trade-console-public/services/api-go"
PUBLIC_MANIFEST = ".public-release-manifest.json"

EXPECTED_PUBLIC_ORIGIN_URLS = {
    "git@github.com:corn12138/wallet-trade-console-public.git",
    "https://github.com/corn12138/wallet-trade-console-public",
    "https://github.com/corn12138/wallet-trade-console-public.git",
}

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
    parser.add_argument(
        "--allow-new-path",
        action="append",
        default=[],
        help="explicit private source file to admit when it is absent from the public baseline",
    )
    parser.add_argument(
        "--allow-delete-path",
        action="append",
        default=[],
        help="explicit existing public source file to remove in this export",
    )
    return parser.parse_args()


def run(*args: str, cwd: Path | None = None, capture: bool = False) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        args,
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def has_control_character(value: str) -> bool:
    return any(ord(character) < 32 or ord(character) == 127 for character in value)


def validate_output(output: Path, source: Path) -> Path:
    resolved = output.expanduser().resolve()
    if resolved == Path(resolved.anchor):
        raise ValueError("output must not be a filesystem root")
    if resolved.exists() and any(resolved.iterdir()):
        raise ValueError(f"output must be empty: {resolved}")
    protected = (source.expanduser().resolve(), PUBLIC_ROOT.resolve())
    if any(
        resolved == root or resolved in root.parents or root in resolved.parents
        for root in protected
    ):
        raise ValueError("output must be separate from the private and public checkouts")

    existing_parent = resolved
    while not existing_parent.exists():
        existing_parent = existing_parent.parent
    git_root = subprocess.run(
        ["git", "-C", str(existing_parent), "rev-parse", "--show-toplevel"],
        check=False,
        capture_output=True,
        text=True,
    )
    if git_root.returncode == 0:
        repository_root = Path(git_root.stdout.strip()).resolve()
        if resolved == repository_root or repository_root in resolved.parents:
            raise ValueError("output must not be inside any Git checkout")
    return resolved


def validate_git_tree_modes(source: Path, revision: str, paths: tuple[str, ...] | None) -> None:
    """Reject Git objects that cannot be safely materialized as regular files."""

    command = ["git", "ls-tree", "-r", "-z", revision]
    if paths:
        command.extend(["--", *paths])
    result = run(*command, cwd=source, capture=True)
    for entry in result.stdout.split(b"\0"):
        if not entry:
            continue
        metadata, raw_path = entry.split(b"\t", 1)
        mode, object_type, _object_id = metadata.split(b" ", 2)
        relative = raw_path.decode()
        if has_control_character(relative):
            raise ValueError("Git tree contains a path with control characters")
        if mode not in {b"100644", b"100755"} or object_type != b"blob":
            raise ValueError(f"Git tree contains a symlink, submodule, or special entry: {relative}")


def validate_regular_tree(root: Path) -> None:
    """Use lstat so no transform can follow links outside an extracted tree."""

    for directory, directory_names, file_names in os.walk(root, followlinks=False):
        current = Path(directory)
        for name in [*directory_names, *file_names]:
            path = current / name
            relative = path.relative_to(root).as_posix()
            if has_control_character(relative) or ".git" in Path(relative).parts:
                raise ValueError(f"tree contains unsafe metadata or path: {relative}")
            mode = path.lstat().st_mode
            if stat.S_ISLNK(mode) or not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
                raise ValueError(f"tree contains a symlink or special file: {relative}")


def extract_archive(
    source: Path,
    revision: str,
    destination: Path,
    archive_name: str,
    paths: tuple[str, ...] | None = None,
) -> None:
    """Extract one immutable Git object without reading working-tree files."""

    validate_git_tree_modes(source, revision, paths)
    archive_path = destination.parent / archive_name
    with archive_path.open("wb") as archive_file:
        command = ["git", "archive", "--format=tar", revision]
        if paths:
            command.extend(["--", *paths])
        subprocess.run(
            command,
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
    validate_regular_tree(destination)


def extract_revision(source: Path, ref: str, destination: Path) -> str:
    source = source.expanduser().resolve()
    result = run("git", "rev-parse", "--show-toplevel", cwd=source, capture=True)
    if Path(result.stdout.decode().strip()).resolve() != source:
        raise ValueError(f"source is not a Git checkout root: {source}")

    resolved_ref = run("git", "rev-parse", "--verify", f"{ref}^{{commit}}", cwd=source, capture=True)
    revision = resolved_ref.stdout.decode().strip()
    # Archive the resolved object, not the caller's movable branch or tag.
    extract_archive(source, revision, destination, "private-source.tar", SOURCE_PATHS)
    return revision


def extract_clean_public_snapshot(destination: Path) -> str:
    """Pin overlays to a clean, committed public checkout."""

    public_root = PUBLIC_ROOT.resolve()
    result = run("git", "rev-parse", "--show-toplevel", cwd=PUBLIC_ROOT, capture=True)
    if Path(result.stdout.decode().strip()).resolve() != public_root:
        raise ValueError(f"public exporter is not at a Git checkout root: {public_root}")

    origin = run("git", "remote", "get-url", "origin", cwd=public_root, capture=True).stdout.decode().strip()
    if origin not in EXPECTED_PUBLIC_ORIGIN_URLS:
        raise ValueError(f"public checkout has an unexpected origin: {origin}")
    branch = run("git", "symbolic-ref", "--short", "HEAD", cwd=public_root, capture=True).stdout.decode().strip()
    if branch != "main":
        raise ValueError(f"public overlays must come from main, not {branch}")

    status = run(
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
        cwd=public_root,
        capture=True,
    )
    if status.stdout:
        raise ValueError("public checkout must be clean before its committed overlays can be used")

    result = run("git", "rev-parse", "--verify", "HEAD^{commit}", cwd=public_root, capture=True)
    revision = result.stdout.decode().strip()
    upstream = run("git", "rev-parse", "--verify", "origin/main^{commit}", cwd=public_root, capture=True)
    if revision != upstream.stdout.decode().strip():
        raise ValueError("public main must match the locally fetched origin/main")
    remote = run(
        "git",
        "ls-remote",
        "--exit-code",
        "origin",
        "refs/heads/main",
        cwd=public_root,
        capture=True,
    ).stdout.decode().split()
    if not remote or revision != remote[0]:
        raise ValueError("public main must match the current remote origin/main")
    extract_archive(public_root, revision, destination, "public-overlay.tar")
    return revision


def copy_path(source_root: Path, destination_root: Path, relative: str) -> None:
    source = source_root / relative
    destination = destination_root / relative
    if not source.exists():
        raise FileNotFoundError(f"required export path is missing: {relative}")
    mode = source.lstat().st_mode
    if stat.S_ISLNK(mode) or not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
        raise ValueError(f"required export path is not a regular file or directory: {relative}")
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


def apply_public_overlays(candidate: Path, public_snapshot: Path) -> None:
    for relative in PUBLIC_OVERLAYS:
        target = candidate / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists() or target.is_symlink():
            target.unlink()
        copy_path(public_snapshot, candidate, relative)


def is_within(relative: str, roots: tuple[str, ...]) -> bool:
    return any(relative == root or relative.startswith(f"{root}/") for root in roots)


def normalize_reviewed_paths(paths: list[str], option: str) -> set[str]:
    normalized: set[str] = set()
    for raw in paths:
        path = PurePosixPath(raw)
        if (
            path.is_absolute()
            or not path.parts
            or any(part in {"", ".", ".."} for part in path.parts)
            or has_control_character(raw)
        ):
            raise ValueError(f"invalid {option}: {raw}")
        relative = path.as_posix()
        if not is_within(relative, SOURCE_PATHS):
            raise ValueError(f"reviewed source path is outside the export roots: {relative}")
        if is_within(relative, PUBLIC_OVERLAYS) or is_within(relative, EXCLUDED_PATHS):
            raise ValueError(f"reviewed source path is public-owned or excluded: {relative}")
        normalized.add(relative)
    return normalized


def source_managed_files(root: Path) -> set[str]:
    files: set[str] = set()
    for path in root.rglob("*"):
        if not path.is_file() or path.is_symlink():
            continue
        relative = path.relative_to(root).as_posix()
        if not is_within(relative, SOURCE_PATHS):
            continue
        if is_within(relative, PUBLIC_OVERLAYS) or is_within(relative, EXCLUDED_PATHS):
            continue
        files.add(relative)
    return files


def validate_source_path_changes(
    candidate: Path,
    public_snapshot: Path,
    allowed_new: set[str],
    allowed_delete: set[str],
) -> None:
    """Require explicit review for both additions and deletions."""

    candidate_files = source_managed_files(candidate)
    public_files = source_managed_files(public_snapshot)
    unreviewed_new = sorted(candidate_files - public_files - allowed_new)
    unreviewed_delete = sorted(public_files - candidate_files - allowed_delete)
    missing_new = sorted(allowed_new - (candidate_files - public_files))
    missing_delete = sorted(allowed_delete - (public_files - candidate_files))

    if missing_new:
        raise ValueError("allowed new paths are not new in this revision: " + ", ".join(missing_new))
    if missing_delete:
        raise ValueError("allowed deletions are not deleted in this revision: " + ", ".join(missing_delete))
    if unreviewed_new:
        paths = "\n".join(f"  --allow-new-path {path}" for path in unreviewed_new)
        raise ValueError(f"private revision contains unreviewed new source paths:\n{paths}")
    if unreviewed_delete:
        paths = "\n".join(f"  --allow-delete-path {path}" for path in unreviewed_delete)
        raise ValueError(f"private revision removes unreviewed public source paths:\n{paths}")


def rewrite_go_module(candidate: Path) -> str:
    validate_regular_tree(candidate)
    go_mod = candidate / "services/api-go/go.mod"
    match = re.search(r"^module\s+(\S+)$", go_mod.read_text(), flags=re.MULTILINE)
    if not match:
        raise ValueError("services/api-go/go.mod has no module declaration")
    private_module = match.group(1)
    if private_module == PUBLIC_GO_MODULE:
        raise ValueError("source already uses the public Go module; expected the private integration module")

    for path in (candidate / "services/api-go").rglob("*"):
        if not path.is_file() or path.stat().st_size > 5_000_000:
            continue
        data = path.read_bytes()
        if b"\0" in data:
            continue
        text = data.decode("utf-8")
        if private_module in text:
            path.write_text(text.replace(private_module, PUBLIC_GO_MODULE))
    return private_module


def validate_go_module_rewrite(candidate: Path, private_module: str) -> None:
    go_mod = candidate / "services/api-go/go.mod"
    if not re.search(rf"^module\s+{re.escape(PUBLIC_GO_MODULE)}$", go_mod.read_text(), flags=re.MULTILINE):
        raise ValueError("public Go module declaration is missing after rewrite")

    needle = private_module.encode()
    for path in candidate.rglob("*"):
        if not path.is_file() or path.is_symlink():
            continue
        data = path.read_bytes()
        if needle in data:
            relative = path.relative_to(candidate).as_posix()
            raise ValueError(f"private Go module remains after rewrite: {relative}")


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


def write_candidate_manifest(candidate: Path, public_revision: str) -> None:
    """Bind every candidate path, mode, and byte sequence before promotion."""

    if not re.fullmatch(r"[0-9a-f]{40}", public_revision):
        raise ValueError("public base revision must be a full commit SHA")
    validate_regular_tree(candidate)
    files: list[dict[str, str]] = []
    for path in sorted(candidate.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(candidate).as_posix()
        if relative == PUBLIC_MANIFEST:
            continue
        mode = "100755" if path.stat().st_mode & stat.S_IXUSR else "100644"
        files.append(
            {
                "path": relative,
                "mode": mode,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            }
        )
    manifest = {"format": 1, "public_base_commit": public_revision, "files": files}
    (candidate / PUBLIC_MANIFEST).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")


def run_trusted_audit(candidate: Path) -> None:
    auditor = PUBLIC_ROOT / "scripts/public_release_audit.py"
    run("python3", "-I", str(auditor), "--root", str(candidate), cwd=PUBLIC_ROOT)


def publish_candidate(staged: Path, output: Path) -> None:
    """Expose the candidate only after all generation gates have passed."""

    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        output.rmdir()
    shutil.move(str(staged), str(output))


def main() -> int:
    args = parse_args()
    source = args.source.expanduser().resolve()
    output = validate_output(args.output, source)
    allowed_new_paths = normalize_reviewed_paths(args.allow_new_path, "--allow-new-path")
    allowed_delete_paths = normalize_reviewed_paths(args.allow_delete_path, "--allow-delete-path")

    with tempfile.TemporaryDirectory(prefix="wallet-public-export-") as temporary:
        temporary_root = Path(temporary)
        raw = temporary_root / "raw"
        public_snapshot = temporary_root / "public"
        staged = temporary_root / "candidate"
        revision = extract_revision(source, args.ref, raw)
        public_revision = extract_clean_public_snapshot(public_snapshot)
        for relative in SOURCE_PATHS:
            copy_path(raw, staged, relative)

        remove_excluded(staged)
        prune_vendor_dependencies(staged)
        validate_regular_tree(staged)
        validate_source_path_changes(
            staged,
            public_snapshot,
            allowed_new_paths,
            allowed_delete_paths,
        )
        apply_public_overlays(staged, public_snapshot)
        validate_regular_tree(staged)
        private_module = rewrite_go_module(staged)
        validate_go_module_rewrite(staged, private_module)
        scrub_generic_test_examples(staged)
        normalize_trailing_whitespace(staged)
        write_candidate_manifest(staged, public_revision)
        run_trusted_audit(staged)
        publish_candidate(staged, output)

    # Gitleaks, trufflehog, dependency checks, builds, and human review still
    # run after this local policy gate and before a public push.
    print(f"public candidate generated from committed revision {revision}")
    print(f"public overlays pinned to committed revision {public_revision}")
    print(f"review and test before publishing: {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
