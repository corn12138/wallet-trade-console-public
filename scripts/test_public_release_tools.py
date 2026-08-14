#!/usr/bin/env python3
"""Regression tests for the fail-closed public release boundary."""

from __future__ import annotations

import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load test module: {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PublicReleaseToolsTest(unittest.TestCase):
    def run_git(self, root: Path, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["git", *args],
            cwd=root,
            check=True,
            text=True,
            capture_output=True,
        )

    def init_repo(self, root: Path) -> None:
        self.run_git(root, "init", "-q")
        self.run_git(root, "config", "user.name", "Public Release Test")
        self.run_git(root, "config", "user.email", "public-release@example.com")

    def configure_public_main(self, root: Path) -> None:
        self.run_git(
            root,
            "remote",
            "add",
            "origin",
            "https://github.com/corn12138/wallet-trade-console-public.git",
        )
        self.run_git(root, "branch", "-M", "main")
        head = self.run_git(root, "rev-parse", "HEAD").stdout.strip()
        self.run_git(root, "update-ref", "refs/remotes/origin/main", head)

    def write_manifest(self, candidate: Path) -> None:
        sync = load_module("sync_manifest_test", SCRIPT_DIR / "sync_from_private.py")
        sync.write_candidate_manifest(candidate)

    def test_clean_public_snapshot_rejects_untracked_overlay(self) -> None:
        sync = load_module("sync_from_private_test", SCRIPT_DIR / "sync_from_private.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.init_repo(root)
            (root / "tracked.txt").write_text("reviewed\n")
            self.run_git(root, "add", "tracked.txt")
            self.run_git(root, "commit", "-qm", "baseline")
            self.configure_public_main(root)
            (root / "unreviewed.txt").write_text("not committed\n")

            sync.PUBLIC_ROOT = root
            with self.assertRaisesRegex(ValueError, "public checkout must be clean"):
                sync.extract_clean_public_snapshot(root / "snapshot")

    def test_new_private_path_requires_explicit_admission(self) -> None:
        sync = load_module("sync_new_path_test", SCRIPT_DIR / "sync_from_private.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate"
            public = root / "public"
            (candidate / "apps/web/src").mkdir(parents=True)
            (public / "apps/web/src").mkdir(parents=True)
            existing = "apps/web/src/existing.ts"
            added = "apps/web/src/new.ts"
            (candidate / existing).write_text("existing\n")
            (public / existing).write_text("existing\n")
            (candidate / added).write_text("new\n")

            with self.assertRaisesRegex(ValueError, "unreviewed new source paths"):
                sync.validate_source_path_changes(candidate, public, set(), set())
            sync.validate_source_path_changes(candidate, public, {added}, set())

            (candidate / existing).unlink()
            with self.assertRaisesRegex(ValueError, "unreviewed public source paths"):
                sync.validate_source_path_changes(candidate, public, {added}, set())
            sync.validate_source_path_changes(candidate, public, {added}, {existing})

    def test_audit_includes_untracked_files(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts"
            scripts.mkdir()
            audit = scripts / "public_release_audit.py"
            audit.write_bytes((SCRIPT_DIR / "public_release_audit.py").read_bytes())
            self.init_repo(root)
            self.run_git(root, "add", "scripts/public_release_audit.py")
            self.run_git(root, "commit", "-qm", "baseline")

            # Construct the token at runtime so the regression fixture itself
            # never resembles a provider credential in the public tree.
            fake_token = "gh" + "p_" + ("A" * 36)
            (root / "untracked.txt").write_text(fake_token)
            result = subprocess.run(
                ["python3", str(audit)],
                cwd=root,
                check=False,
                text=True,
                capture_output=True,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("github-token: untracked.txt", result.stderr)

    def test_candidate_promotion_preserves_git_and_deletes_stale_files(self) -> None:
        promote = load_module("promote_candidate_test", SCRIPT_DIR / "promote_public_candidate.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate"
            target = root / "target"
            candidate.mkdir()
            target.mkdir()
            (candidate / "kept.txt").write_text("new\n")
            (target / "kept.txt").write_text("old\n")
            (target / "stale.txt").write_text("remove\n")
            (target / ".git").write_text("gitdir: elsewhere\n")

            promote.replace_tree(candidate, target)

            self.assertEqual((target / "kept.txt").read_text(), "new\n")
            self.assertFalse((target / "stale.txt").exists())
            self.assertTrue((target / ".git").is_file())

    def test_symlink_is_rejected_before_module_rewrite(self) -> None:
        sync = load_module("sync_symlink_test", SCRIPT_DIR / "sync_from_private.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate"
            module = candidate / "services/api-go/go.mod"
            module.parent.mkdir(parents=True)
            external = root / "external.txt"
            external.write_text("module private.example/module\n")
            module.symlink_to(external)

            with self.assertRaisesRegex(ValueError, "symlink or special file"):
                sync.rewrite_go_module(candidate)
            self.assertEqual(external.read_text(), "module private.example/module\n")

    def test_directory_symlink_is_rejected_by_audit_and_promotion(self) -> None:
        promote = load_module("promote_symlink_test", SCRIPT_DIR / "promote_public_candidate.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate"
            external = root / "external"
            candidate.mkdir()
            external.mkdir()
            (external / "outside.txt").write_text("outside\n")
            (candidate / "linked").symlink_to(external, target_is_directory=True)

            with self.assertRaisesRegex(ValueError, "symlink or special directory"):
                promote.validate_regular_tree(candidate)

            result = subprocess.run(
                [
                    "python3",
                    str(SCRIPT_DIR / "public_release_audit.py"),
                    "--root",
                    str(candidate),
                ],
                check=False,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("symlink: linked", result.stderr)

    def test_candidate_cannot_supply_its_own_noop_auditor(self) -> None:
        promote = load_module("promote_trusted_audit_test", SCRIPT_DIR / "promote_public_candidate.py")
        with tempfile.TemporaryDirectory() as temporary:
            candidate = Path(temporary) / "candidate"
            (candidate / ".github/workflows").mkdir(parents=True)
            (candidate / "scripts").mkdir()
            (candidate / "services/api-go").mkdir(parents=True)
            (candidate / "README.md").write_text("candidate\n")
            (candidate / ".github/workflows/public-ci.yml").write_text(
                "permissions:\n  contents: read\n"
            )
            (candidate / "scripts/public_release_audit.py").write_text("raise SystemExit(0)\n")
            (candidate / "services/api-go/go.mod").write_text("module private.example/module\n")
            (candidate / ".env").write_text("NOT_PUBLIC=filled\n")
            self.write_manifest(candidate)

            with self.assertRaises(subprocess.CalledProcessError):
                promote.validate_candidate(candidate)

    def test_manifest_tamper_and_unreviewed_deletion_are_rejected(self) -> None:
        promote = load_module("promote_manifest_test", SCRIPT_DIR / "promote_public_candidate.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate"
            target = root / "target"
            candidate.mkdir()
            target.mkdir()
            (candidate / "kept.txt").write_text("candidate\n")
            (target / "kept.txt").write_text("old\n")
            (target / "removed.txt").write_text("must be reviewed\n")
            self.write_manifest(candidate)

            files = promote.validate_regular_tree(candidate)
            promote.validate_manifest(candidate, files)
            with self.assertRaisesRegex(ValueError, "unreviewed deletions"):
                promote.validate_deletions(candidate, target, set())
            promote.validate_deletions(candidate, target, {"removed.txt"})

            (candidate / "kept.txt").write_text("tampered\n")
            with self.assertRaisesRegex(ValueError, "does not match"):
                promote.validate_manifest(candidate, promote.validate_regular_tree(candidate))

    def test_public_overlay_requires_expected_origin_main_and_upstream(self) -> None:
        sync = load_module("sync_identity_test", SCRIPT_DIR / "sync_from_private.py")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.init_repo(root)
            (root / "tracked.txt").write_text("reviewed\n")
            self.run_git(root, "add", "tracked.txt")
            self.run_git(root, "commit", "-qm", "baseline")
            sync.PUBLIC_ROOT = root

            self.run_git(root, "remote", "add", "origin", "https://example.com/wrong.git")
            with self.assertRaisesRegex(ValueError, "unexpected origin"):
                sync.extract_clean_public_snapshot(root / "wrong-origin")

            self.run_git(root, "remote", "set-url", "origin", "https://github.com/corn12138/wallet-trade-console-public.git")
            self.run_git(root, "branch", "-M", "feature")
            head = self.run_git(root, "rev-parse", "HEAD").stdout.strip()
            self.run_git(root, "update-ref", "refs/remotes/origin/main", head)
            with self.assertRaisesRegex(ValueError, "must come from main"):
                sync.extract_clean_public_snapshot(root / "wrong-branch")

            self.run_git(root, "branch", "-M", "main")
            (root / "tracked.txt").write_text("new local commit\n")
            self.run_git(root, "add", "tracked.txt")
            self.run_git(root, "commit", "-qm", "local ahead")
            with self.assertRaisesRegex(ValueError, "must match"):
                sync.extract_clean_public_snapshot(root / "stale-main")


if __name__ == "__main__":
    unittest.main()
