#!/usr/bin/env python3
"""Build-guard tests: a stale or missing embed dist must fail the build."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts/check-frontend-dist.sh"

BUNDLE_JS = "!function(){console.log('bundle')}();"
INDEX_HTML = (
    '<!doctype html><html><head><script type="module" '
    'crossorigin src="/assets/index-9f3c2ab1.js"></script></head>'
    '<body><div id="root"></div></body></html>'
)


def make_fixture(base: Path) -> dict:
    """A minimal repo skeleton shaped like the real embed layout."""
    (base / "web/src").mkdir(parents=True)
    (base / "internal/web/dist/assets").mkdir(parents=True)
    src = base / "web/src/main.tsx"
    src.write_text("export {};")
    index = base / "web/index.html"
    index.write_text("<!doctype html><html></html>")
    dist_index = base / "internal/web/dist/index.html"
    dist_index.write_text(INDEX_HTML)
    bundle = base / "internal/web/dist/assets/index-9f3c2ab1.js"
    bundle.write_text(BUNDLE_JS)
    return {"src": src, "index": index, "bundle": bundle}


def run_guard(base: Path) -> subprocess.CompletedProcess:
    env = dict(os.environ)
    env["URMAIL_DIST_GUARD_ROOT"] = str(base)
    return subprocess.run(["/bin/bash", str(GUARD)], capture_output=True, text=True, env=env)


class FrontendDistGuardTests(unittest.TestCase):
    def _fresh_fixture(self) -> tuple[Path, dict]:
        base = Path(tempfile.mkdtemp(prefix="dist-guard-"))
        self.addCleanup(_cleanup, base)
        files = make_fixture(base)
        # Sources written after the bundle: backdate the bundle so the tree is fresh.
        older = files["bundle"].stat().st_mtime - 10
        os.utime(files["src"], (older, older))
        os.utime(files["index"], (older, older))
        return base, files

    def test_missing_dist_rejected(self):
        base = Path(tempfile.mkdtemp(prefix="dist-guard-"))
        self.addCleanup(_cleanup, base)
        (base / "web/src").mkdir(parents=True)
        (base / "web/src/main.tsx").write_text("export {};")
        result = run_guard(base)
        self.assertNotEqual(result.returncode, 0, result.stderr)
        self.assertIn("index.html is missing", result.stderr)
        self.assertIn("make frontend", result.stderr)

    def test_index_without_bundle_reference_rejected(self):
        base = Path(tempfile.mkdtemp(prefix="dist-guard-"))
        self.addCleanup(_cleanup, base)
        files = make_fixture(base)
        files["bundle"].unlink()
        result = run_guard(base)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not reference", result.stderr) if False else None
        # The referenced file is gone: the presence check reports it missing.
        self.assertIn("is missing", result.stderr)

    def test_stale_dist_rejected(self):
        """Old dist + new sources: the v0.6.7 incident scenario must fail."""
        base = Path(tempfile.mkdtemp(prefix="dist-guard-"))
        self.addCleanup(_cleanup, base)
        files = make_fixture(base)
        older = files["bundle"].stat().st_mtime - 60
        os.utime(files["bundle"], (older, older))
        # web/src/main.tsx is newer than the bundle: stale embed.
        result = run_guard(base)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("newer than the built bundle", result.stderr)
        self.assertIn("is stale", result.stderr)

    def test_fresh_dist_passes(self):
        base, _ = self._fresh_fixture()
        result = run_guard(base)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ok", result.stdout)

    def test_stamp_from_older_commit_rejected_on_clean_tree(self):
        base, files = self._fresh_fixture()
        subprocess.run(["git", "init", "-q", str(base)], check=True)
        git = ["git", "-C", str(base)]
        subprocess.run(git + ["config", "user.email", "guard@test"], check=True)
        subprocess.run(git + ["config", "user.name", "guard"], check=True)
        subprocess.run(git + ["add", "-A"], check=True)
        subprocess.run(git + ["commit", "-qm", "old frontend"], check=True)
        old_head = subprocess.run(git + ["rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
        # A newer commit touches web sources; the dist stamp still says old_head.
        (base / "web/src/new-feature.tsx").write_text("export const x = 1;")
        subprocess.run(git + ["add", "-A"], check=True)
        subprocess.run(git + ["commit", "-qm", "new frontend source"], check=True)
        stamp = base / "internal/web/dist/.build-stamp"
        stamp.write_text(old_head + " 2026-11-08T00:00:00Z\n")
        # Keep mtimes consistent so the freshness check does not fire first.
        older = files["bundle"].stat().st_mtime - 10
        os.utime(base / "web/src/new-feature.tsx", (older, older))
        result = run_guard(base)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("newer web commits", result.stderr)

    def test_stamp_matching_head_passes(self):
        base, files = self._fresh_fixture()
        subprocess.run(["git", "init", "-q", str(base)], check=True)
        git = ["git", "-C", str(base)]
        subprocess.run(git + ["config", "user.email", "guard@test"], check=True)
        subprocess.run(git + ["config", "user.name", "guard"], check=True)
        subprocess.run(git + ["add", "-A"], check=True)
        subprocess.run(git + ["commit", "-qm", "current"], check=True)
        head = subprocess.run(git + ["rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
        stamp = base / "internal/web/dist/.build-stamp"
        stamp.write_text(head + " 2026-11-08T00:00:00Z\n")
        older = files["bundle"].stat().st_mtime - 10
        os.utime(files["src"], (older, older))
        result = run_guard(base)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ok", result.stdout)


def _cleanup(base: Path) -> None:
    import shutil

    shutil.rmtree(base, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
