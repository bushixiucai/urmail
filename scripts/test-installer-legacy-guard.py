#!/usr/bin/env python3
"""Exercise the exact read-only installer guard against private fixtures."""
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "installer/install.sh"


class LegacyInstallationGuardTests(unittest.TestCase):
    def test_exact_guard_before_mutation(self):
        self.exercise_guard(SOURCE.read_text(), installer=True)

    def test_package_preinst_same_guard(self):
        build = SOURCE.parent.parent / "packaging/deb/build.sh"
        source = build.read_text()
        _, script = source.split("cat >\"$STAGE/DEBIAN/preinst\" <<'EOF'\n", 1)
        script, _ = script.split("\nEOF", 1)
        self.exercise_guard(script, installer=False)
        self.assertIn('chmod 0755 "$STAGE/DEBIAN/preinst"', source)

    def exercise_guard(self, source, installer):
        begin = source.index("# BEGIN LEGACY INSTALLATION GUARD")
        end = source.index("# END LEGACY INSTALLATION GUARD", begin)
        if installer:
            self.assertLess(end, source.index("apt-get update"))
            self.assertLess(end, source.index("dpkg -i"))
            self.assertLess(end, source.index("systemctl stop"))
        else:
            canonical = SOURCE.read_text()
            a = canonical.index("# BEGIN LEGACY INSTALLATION GUARD")
            b = canonical.index("# END LEGACY INSTALLATION GUARD", a)
            guard_code = lambda text: [line.strip() for line in text.splitlines()
                                       if line.strip() and not line.lstrip().startswith("#")]
            self.assertEqual(guard_code(source[begin:end]), guard_code(canonical[a:b]))
        block = source[begin:end]
        legacy = ("etc/mailinabox.conf", "home/user-data/mail/users.sqlite",
                  "var/lib/mailinabox/mail/users.sqlite")
        scenarios = ["fresh", "managed-only", *legacy, "mixed", "dangling-link"]
        for scenario in scenarios:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as root:
                base = Path(root)
                if scenario in ("managed-only", "mixed"):
                    target = base / "etc/urmail/urmaild.env"
                    target.parent.mkdir(parents=True)
                    target.write_text("URMAIL_DATABASE_URL=private-fixture-never-logged\n")
                if scenario in legacy or scenario == "mixed":
                    target = base / (scenario if scenario in legacy else legacy[0])
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text("private-fixture-never-logged")
                if scenario == "dangling-link":
                    target = base / legacy[0]
                    target.parent.mkdir(parents=True)
                    target.symlink_to(base / "missing-private-source")
                fixture_block = block
                for name in legacy:
                    fixture_block = fixture_block.replace("/" + name, str(base / name))
                script = 'set -eu\ndie() { printf "%s\\n" "$*" >&2; exit 42; }\n' + fixture_block
                result = subprocess.run(["/bin/bash" if installer else "/bin/sh", "-c", script], text=True,
                                        capture_output=True, check=False)
                self.assertEqual(result.returncode, 0 if scenario in ("fresh", "managed-only") else 42)
                self.assertNotIn("private-fixture-never-logged", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
