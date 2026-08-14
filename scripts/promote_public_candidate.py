#!/usr/bin/env python3
"""Replace a disposable public worktree with a reviewed export candidate.

The command never commits or pushes. It deliberately requires a clean
``public-sync/*`` branch based exactly on ``origin/main`` so deletion semantics
cannot target the maintainer's long-lived checkout by accident.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import stat
import subprocess
from pathlib import Path


EXPECTED_ORIGIN_URLS = {
    "git@github.com:corn12138/wallet-trade-console-public.git",
    "https://github.com/corn12138/wallet-trade-console-public",
    "https://github.com/corn12138/wallet-trade-console-public.git",
}

TRUSTED_ROOT = Path(__file__).resolve().parents[1]
TRUSTED_AUDITOR = TRUSTED_ROOT / "scripts/public_release_audit.py"
PUBLIC_MANIFEST = ".public-release-manifest.json"

REQUIRED_CANDIDATE_PATHS = (
    PUBLIC_MANIFEST,
    ".github/workflows/public-ci.yml",
    "README.md",
    "scripts/public_release_audit.py",
    "services/api-go/go.mod",
)


def run(*args: str, cwd: Path, capture: bool = False) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        args,
        cwd=cwd,
        check=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path, help="complete audited export tree")
    parser.add_argument("--target", required=True, type=Path, help="disposable public Git worktree")
    parser.add_argument(
        "--allow-delete-path",
        action="append",
        default=[],
        help="exact target file approved for deletion during this promotion",
    )
    return parser.parse_args()


def has_control_character(value: str) -> bool:
    return any(ord(character) < 32 or ord(character) == 127 for character in value)


def validate_regular_tree(root: Path, allow_root_git: bool = False) -> set[str]:
    """Return regular files without following links or special entries."""

    files: set[str] = set()
    for directory, directory_names, file_names in os.walk(root, followlinks=False):
        current = Path(directory)
        for name in list(directory_names):
            path = current / name
            relative = path.relative_to(root).as_posix()
            if allow_root_git and relative == ".git":
                directory_names.remove(name)
                continue
            mode = path.lstat().st_mode
            if has_control_character(relative) or ".git" in Path(relative).parts:
                raise ValueError(f"tree contains unsafe metadata or path: {relative}")
            if stat.S_ISLNK(mode) or not stat.S_ISDIR(mode):
                raise ValueError(f"tree contains a symlink or special directory entry: {relative}")
        for name in file_names:
            path = current / name
            relative = path.relative_to(root).as_posix()
            if allow_root_git and relative == ".git":
                continue
            mode = path.lstat().st_mode
            if has_control_character(relative) or ".git" in Path(relative).parts:
                raise ValueError(f"tree contains unsafe metadata or path: {relative}")
            if not stat.S_ISREG(mode):
                raise ValueError(f"tree contains a symlink or special file: {relative}")
            files.add(relative)
    return files


def validate_trusted_checkout() -> None:
    root = TRUSTED_ROOT.resolve()
    result = run("git", "rev-parse", "--show-toplevel", cwd=root, capture=True)
    if Path(result.stdout.decode().strip()).resolve() != root:
        raise ValueError(f"promotion helper is not at a Git checkout root: {root}")
    origin = run("git", "remote", "get-url", "origin", cwd=root, capture=True).stdout.decode().strip()
    if origin not in EXPECTED_ORIGIN_URLS:
        raise ValueError(f"promotion helper has an unexpected origin: {origin}")
    branch = run("git", "symbolic-ref", "--short", "HEAD", cwd=root, capture=True).stdout.decode().strip()
    if branch != "main":
        raise ValueError(f"promotion helper must run from reviewed main, not {branch}")
    head = run("git", "rev-parse", "HEAD^{commit}", cwd=root, capture=True).stdout
    upstream = run("git", "rev-parse", "origin/main^{commit}", cwd=root, capture=True).stdout
    if head != upstream:
        raise ValueError("promotion helper main must match the locally fetched origin/main")
    remote = run(
        "git",
        "ls-remote",
        "--exit-code",
        "origin",
        "refs/heads/main",
        cwd=root,
        capture=True,
    ).stdout.split()
    if not remote or head.strip() != remote[0]:
        raise ValueError("promotion helper main must match the current remote origin/main")
    status = run(
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
        cwd=root,
        capture=True,
    )
    if status.stdout:
        raise ValueError("promotion helper checkout must be clean")


def validate_manifest(candidate: Path, files: set[str]) -> None:
    manifest_path = candidate / PUBLIC_MANIFEST
    if manifest_path.stat().st_size > 5_000_000:
        raise ValueError("candidate manifest is unexpectedly large")
    manifest = json.loads(manifest_path.read_text())
    if not isinstance(manifest, dict) or set(manifest) != {"format", "files"} or manifest["format"] != 1:
        raise ValueError("candidate manifest has an unsupported structure")
    if not isinstance(manifest["files"], list):
        raise ValueError("candidate manifest files must be a list")

    expected_paths: set[str] = set()
    for entry in manifest["files"]:
        if not isinstance(entry, dict) or set(entry) != {"mode", "path", "sha256"}:
            raise ValueError("candidate manifest contains an invalid file entry")
        relative = entry["path"]
        if (
            not isinstance(relative, str)
            or has_control_character(relative)
            or Path(relative).is_absolute()
            or ".." in Path(relative).parts
            or relative == PUBLIC_MANIFEST
            or relative in expected_paths
        ):
            raise ValueError("candidate manifest contains an unsafe or duplicate path")
        path = candidate / relative
        if relative not in files:
            raise ValueError(f"candidate manifest references a missing file: {relative}")
        expected_mode = "100755" if path.stat().st_mode & stat.S_IXUSR else "100644"
        expected_hash = hashlib.sha256(path.read_bytes()).hexdigest()
        if entry["mode"] != expected_mode or entry["sha256"] != expected_hash:
            raise ValueError(f"candidate manifest does not match file bytes or mode: {relative}")
        expected_paths.add(relative)

    actual_paths = files - {PUBLIC_MANIFEST}
    if expected_paths != actual_paths:
        missing = sorted(actual_paths - expected_paths)
        raise ValueError("candidate manifest omits files: " + ", ".join(missing))


def run_trusted_audit(root: Path) -> None:
    run("python3", "-I", str(TRUSTED_AUDITOR), "--root", str(root), cwd=TRUSTED_ROOT)


def validate_candidate(candidate: Path) -> Path:
    candidate = candidate.expanduser().resolve()
    if not candidate.is_dir() or candidate == Path(candidate.anchor):
        raise ValueError(f"candidate is not a safe directory: {candidate}")
    if (candidate / ".git").exists():
        raise ValueError("candidate must be a generated tree without Git metadata")
    missing = [relative for relative in REQUIRED_CANDIDATE_PATHS if not (candidate / relative).is_file()]
    if missing:
        raise ValueError("candidate is missing required paths: " + ", ".join(missing))
    files = validate_regular_tree(candidate)
    validate_manifest(candidate, files)
    run_trusted_audit(candidate)
    return candidate


def validate_target(target: Path) -> Path:
    target = target.expanduser().resolve()
    result = run("git", "rev-parse", "--show-toplevel", cwd=target, capture=True)
    if Path(result.stdout.decode().strip()).resolve() != target:
        raise ValueError(f"target is not a Git checkout root: {target}")

    origin = run("git", "remote", "get-url", "origin", cwd=target, capture=True).stdout.decode().strip()
    if origin not in EXPECTED_ORIGIN_URLS:
        raise ValueError(f"target origin is not the public repository: {origin}")

    branch = run("git", "symbolic-ref", "--short", "HEAD", cwd=target, capture=True).stdout.decode().strip()
    if not branch.startswith("public-sync/"):
        raise ValueError(f"target branch must use the public-sync/ prefix: {branch}")

    head = run("git", "rev-parse", "HEAD^{commit}", cwd=target, capture=True).stdout
    upstream = run("git", "rev-parse", "origin/main^{commit}", cwd=target, capture=True).stdout
    if head != upstream:
        raise ValueError("target must start exactly at the locally fetched origin/main")
    remote = run(
        "git",
        "ls-remote",
        "--exit-code",
        "origin",
        "refs/heads/main",
        cwd=target,
        capture=True,
    ).stdout.split()
    if not remote or head.strip() != remote[0]:
        raise ValueError("target must start exactly at the current remote origin/main")

    status = run(
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
        cwd=target,
        capture=True,
    )
    ignored = run(
        "git",
        "ls-files",
        "--others",
        "--ignored",
        "--exclude-standard",
        "-z",
        cwd=target,
        capture=True,
    )
    if status.stdout or ignored.stdout:
        raise ValueError("target worktree must contain no tracked, untracked, or ignored changes")
    validate_regular_tree(target, allow_root_git=True)
    return target


def normalize_allowed_deletions(paths: list[str]) -> set[str]:
    allowed: set[str] = set()
    for raw in paths:
        path = Path(raw)
        if path.is_absolute() or not path.parts or ".." in path.parts or has_control_character(raw):
            raise ValueError(f"invalid --allow-delete-path: {raw}")
        allowed.add(path.as_posix())
    return allowed


def validate_deletions(candidate: Path, target: Path, allowed: set[str]) -> None:
    candidate_files = validate_regular_tree(candidate)
    target_files = validate_regular_tree(target, allow_root_git=True)
    deletions = target_files - candidate_files
    unreviewed = sorted(deletions - allowed)
    unused = sorted(allowed - deletions)
    if unused:
        raise ValueError("approved deletion paths are not deleted by the candidate: " + ", ".join(unused))
    if unreviewed:
        commands = "\n".join(f"  --allow-delete-path {path}" for path in unreviewed)
        raise ValueError(f"candidate contains unreviewed deletions:\n{commands}")


def replace_tree(candidate: Path, target: Path) -> None:
    """Apply the complete candidate while preserving only target Git metadata."""

    for child in target.iterdir():
        if child.name == ".git":
            continue
        if child.is_dir() and not child.is_symlink():
            shutil.rmtree(child)
        else:
            child.unlink()

    for child in candidate.iterdir():
        destination = target / child.name
        if child.is_dir():
            shutil.copytree(child, destination, symlinks=True)
        else:
            shutil.copy2(child, destination)


def main() -> int:
    args = parse_args()
    validate_trusted_checkout()
    candidate = validate_candidate(args.candidate)
    target = validate_target(args.target)
    if candidate == target or target in candidate.parents or candidate in target.parents:
        raise ValueError("candidate and target must be separate directory trees")
    allowed_deletions = normalize_allowed_deletions(args.allow_delete_path)
    validate_deletions(candidate, target, allowed_deletions)

    replace_tree(candidate, target)
    files = validate_regular_tree(target, allow_root_git=True)
    validate_manifest(target, files)
    run_trusted_audit(target)
    status = run("git", "status", "--short", cwd=target, capture=True).stdout.decode()
    print("candidate promoted; review every A/M/D entry before commit:")
    print(status, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
