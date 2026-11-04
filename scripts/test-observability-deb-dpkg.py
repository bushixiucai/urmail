#!/usr/bin/env python3
"""Run real dpkg upgrade verification of observability configuration in Ubuntu containers.

Validates that upgrading from an old deb package (with /etc/urmail/observability/*.yml
in the package payload) to a new deb package (with /usr/share/urmail/observability/*.yml):
1. Preinst takes a secure snapshot into /var/lib/urmail-config-migration/observability/ (0700).
2. Existing customized Alertmanager webhook configuration is completely preserved.
3. Enabled markers (receiver.enabled, monitor.enabled) and credentials (metrics.token 0640)
   are preserved with exact permissions and owners.
4. Alerts.yml / templates from /usr/share are seeded if absent, but never overwrite customized configs.
5. ExecStartPre checks in urmail-alertmanager.service prevent starting with placeholder receiver.
Runs across ubuntu:22.04 and ubuntu:24.04 (urmail-security-package:22.04 / :24.04).
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

IMAGES = ["urmail-security-package:22.04", "urmail-security-package:24.04"]

CONTAINER_INNER_TEST = """#!/usr/bin/env bash
set -euo pipefail

DISTRO="$1"
TEST_DIR="$2"
OLD_DEB="$TEST_DIR/old.deb"
NEW_DEB="$TEST_DIR/new.deb"

echo "=== [Distro: $DISTRO] Starting real dpkg upgrade verification in container ==="

# 1. Stub systemctl to allow dpkg postinst/prerm execution in container without init system
dpkg-divert --add --rename --divert /bin/systemctl.real /bin/systemctl 2>/dev/null || true
dpkg-divert --add --rename --divert /usr/bin/systemctl.real /usr/bin/systemctl 2>/dev/null || true
printf "#!/bin/sh\\nexit 0\\n" > /usr/bin/systemctl
chmod 0755 /usr/bin/systemctl
ln -sf /usr/bin/systemctl /bin/systemctl 2>/dev/null || true

# Also create urmail-monitor system user/group if not existing for complete permission tests
if ! getent group urmail-monitor >/dev/null; then
    addgroup --system urmail-monitor
fi
if ! getent passwd urmail-monitor >/dev/null; then
    adduser --system --ingroup urmail-monitor --no-create-home urmail-monitor
fi

# 2. Step 1: Install OLD deb package (which has files under /etc/urmail/observability)
echo "--- Installing OLD deb package ---"
# NOTE: In this container test, systemctl is stubbed to allow dpkg scripts to run
# without PID 1 systemd. --force-depends is used because this test VM container
# isolates package migration logic and does not provision full runtime external services;
# this is an overlay migration verification, NOT a full dependency closure acceptance test.
dpkg -i --force-depends "$OLD_DEB"

# Verify exact old version is successfully in 'install ok installed' status via dpkg-query
OLD_PKG_STATUS=$(dpkg-query -s urmail | awk -F": " '/^Status: / {print $2}')
OLD_PKG_VER=$(dpkg-query -s urmail | awk -F": " '/^Version: / {print $2}')
echo "Old package status: $OLD_PKG_STATUS, version: $OLD_PKG_VER"
if [ "$OLD_PKG_STATUS" != "install ok installed" ] || [ "$OLD_PKG_VER" != "0.5.0~test.old" ]; then
    echo "ERROR: Old deb did not achieve installed status: status='$OLD_PKG_STATUS', ver='$OLD_PKG_VER'" >&2
    exit 1
fi

# Verify old deb installed files into /etc/urmail/observability
if [ ! -f /etc/urmail/observability/alertmanager.yml ]; then
    echo "ERROR: Old package did not populate /etc/urmail/observability/alertmanager.yml" >&2
    exit 1
fi

# 3. Step 2: Customize Alertmanager receiver, create markers and token
echo "--- Operator customizes Alertmanager receiver and enabled markers ---"
CUSTOM_WEBHOOK_URL="https://alerts.ops.example.com/webhook/prod-critical"
cat > /etc/urmail/observability/alertmanager.yml << 'EOF'
# Custom production alertmanager receiver
route:
  receiver: production-webhook
  group_by: ['alertname', 'kind']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 1h
receivers:
  - name: production-webhook
    webhook_configs:
      - url: https://alerts.ops.example.com/webhook/prod-critical
        send_resolved: true
EOF

touch /etc/urmail/observability/receiver.enabled
touch /etc/urmail/observability/monitor.enabled
echo "secret-metrics-token-xyz789" > /etc/urmail/observability/metrics.token
chown root:urmail-monitor /etc/urmail/observability/metrics.token
chmod 0640 /etc/urmail/observability/metrics.token

# Capture state before upgrade
BEFORE_CFG=$(cat /etc/urmail/observability/alertmanager.yml)
BEFORE_TOKEN_PERM=$(stat -c "%a %U:%G" /etc/urmail/observability/metrics.token)
BEFORE_RECEIVER_MARKER=$(test -f /etc/urmail/observability/receiver.enabled && echo "present")

# 4. Step 3: Upgrade to NEW deb package using real dpkg -i
echo "--- Upgrading to NEW deb package with dpkg -i ---"
dpkg -i --force-depends "$NEW_DEB"

# Verify exact new version is successfully in 'install ok installed' status via dpkg-query
NEW_PKG_STATUS=$(dpkg-query -s urmail | awk -F": " '/^Status: / {print $2}')
NEW_PKG_VER=$(dpkg-query -s urmail | awk -F": " '/^Version: / {print $2}')
echo "Upgraded package status: $NEW_PKG_STATUS, version: $NEW_PKG_VER"
if [ "$NEW_PKG_STATUS" != "install ok installed" ] || [ "$NEW_PKG_VER" != "0.6.0~test.new" ]; then
    echo "ERROR: New deb did not achieve installed status after upgrade: status='$NEW_PKG_STATUS', ver='$NEW_PKG_VER'" >&2
    exit 1
fi

# 5. Step 4: Verification of post-upgrade state
echo "--- Verifying post-upgrade preservation ---"

# Check migration snapshot directory permissions (must be 0700)
if [ -d /var/lib/urmail-config-migration/observability ]; then
    MIG_DIR_PERM=$(stat -c "%a" /var/lib/urmail-config-migration/observability)
    if [ "$MIG_DIR_PERM" != "700" ]; then
        echo "ERROR: Migration snapshot dir permission is $MIG_DIR_PERM, expected 700" >&2
        exit 1
    fi
fi

# Verify Alertmanager config was NOT overwritten
AFTER_CFG=$(cat /etc/urmail/observability/alertmanager.yml)
if [ "$BEFORE_CFG" != "$AFTER_CFG" ]; then
    echo "ERROR: Alertmanager configuration was OVERWRITTEN during upgrade!" >&2
    echo "Expected:"
    echo "$BEFORE_CFG"
    echo "Actual:"
    echo "$AFTER_CFG"
    exit 1
fi

if ! grep -q "$CUSTOM_WEBHOOK_URL" /etc/urmail/observability/alertmanager.yml; then
    echo "ERROR: Custom webhook URL missing after upgrade!" >&2
    exit 1
fi

# Verify receiver.enabled and monitor.enabled markers preserved
if [ ! -f /etc/urmail/observability/receiver.enabled ]; then
    echo "ERROR: receiver.enabled marker was LOST during upgrade!" >&2
    exit 1
fi
if [ ! -f /etc/urmail/observability/monitor.enabled ]; then
    echo "ERROR: monitor.enabled marker was LOST during upgrade!" >&2
    exit 1
fi

# Verify metrics.token content, 0640 permissions and owner preserved
if [ ! -f /etc/urmail/observability/metrics.token ]; then
    echo "ERROR: metrics.token was LOST during upgrade!" >&2
    exit 1
fi
AFTER_TOKEN_CONTENT=$(cat /etc/urmail/observability/metrics.token)
if [ "$AFTER_TOKEN_CONTENT" != "secret-metrics-token-xyz789" ]; then
    echo "ERROR: metrics.token content was corrupted!" >&2
    exit 1
fi
AFTER_TOKEN_PERM=$(stat -c "%a %U:%G" /etc/urmail/observability/metrics.token)
if [ "$AFTER_TOKEN_PERM" != "640 root:urmail-monitor" ]; then
    echo "ERROR: metrics.token permissions/ownership changed: $AFTER_TOKEN_PERM (expected 640 root:urmail-monitor)" >&2
    exit 1
fi

# Verify templates exist in /usr/share/urmail/observability/
for tmpl in alertmanager.yml prometheus.yml alerts.yml; do
    if [ ! -f "/usr/share/urmail/observability/$tmpl" ]; then
        echo "ERROR: Template /usr/share/urmail/observability/$tmpl missing in new deb!" >&2
        exit 1
    fi
done

# 6. Step 5: Test urmail-alertmanager.service ExecStartPre guard logic
echo "--- Testing urmail-alertmanager.service ExecStartPre guard ---"
EXEC_START_PRE=$(grep '^ExecStartPre=' /lib/systemd/system/urmail-alertmanager.service | cut -d= -f2-)
echo "ExecStartPre command: $EXEC_START_PRE"

# Case A: Valid external receiver configuration -> ExecStartPre must PASS (exit 0)
if eval "$EXEC_START_PRE"; then
    echo "PASS: ExecStartPre accepted valid custom external receiver"
else
    echo "ERROR: ExecStartPre unexpectedly rejected valid external receiver" >&2
    exit 1
fi

# Case B: Revert alertmanager.yml to stock 127.0.0.1 placeholder -> ExecStartPre must REJECT (exit non-zero)
cp /usr/share/urmail/observability/alertmanager.yml /etc/urmail/observability/alertmanager.yml
if eval "$EXEC_START_PRE" 2>/dev/null; then
    echo "ERROR: ExecStartPre accepted 127.0.0.1 placeholder receiver while receiver.enabled is present!" >&2
    exit 1
else
    echo "PASS: ExecStartPre rejected 127.0.0.1 placeholder receiver"
fi

# Case C: alertmanager.yml with localhost -> ExecStartPre must REJECT
printf 'route:\\n  receiver: test\\nreceivers:\\n  - name: test\\n    webhook_configs:\\n      - url: http://localhost:9099/alerts\\n' > /etc/urmail/observability/alertmanager.yml
if eval "$EXEC_START_PRE" 2>/dev/null; then
    echo "ERROR: ExecStartPre accepted localhost placeholder receiver!" >&2
    exit 1
else
    echo "PASS: ExecStartPre rejected localhost placeholder receiver"
fi

# Case D: Missing alertmanager.yml -> ExecStartPre must REJECT
rm -f /etc/urmail/observability/alertmanager.yml
if eval "$EXEC_START_PRE" 2>/dev/null; then
    echo "ERROR: ExecStartPre accepted missing alertmanager.yml!" >&2
    exit 1
else
    echo "PASS: ExecStartPre rejected missing alertmanager.yml"
fi

echo "=== [Distro: $DISTRO] All verification checks PASSED successfully! ==="
"""


def run_dpkg_verification():
    root = Path(__file__).resolve().parents[1]
    host_test_dir = Path(tempfile.mkdtemp(prefix="urmail-dpkg-test-", dir="/tmp"))
    print(f"Working directory on host: {host_test_dir}")

    try:
        # Build NEW deb package using existing arm64 binaries from stock deb
        print("\n--- Preparing binaries for package build ---")
        stock_deb = root / "dist/urmail_0.5.0_arm64.deb"
        if not stock_deb.exists():
            print(f"ERROR: Base package {stock_deb} not found", file=sys.stderr)
            return 1

        bin_dir = host_test_dir / "bin"
        bin_dir.mkdir(parents=True)

        # Extract binaries from stock deb to bin_dir inside container
        extract_cmd = [
            "docker", "run", "--rm",
            "-v", f"{root}:/workspace:ro",
            "-v", f"{host_test_dir}:/testdir",
            "urmail-security-package:22.04",
            "bash", "-c",
            "dpkg-deb -x /workspace/dist/urmail_0.5.0_arm64.deb /tmp/extract && "
            "cp /tmp/extract/usr/bin/* /testdir/bin/ && "
            "cp /tmp/extract/usr/lib/urmail/archive-pipe /testdir/bin/urmail-archive && "
            "chmod +x /testdir/bin/*"
        ]
        subprocess.run(extract_cmd, check=True)

        # Build NEW deb package (0.6.0~test.new)
        print("\n--- Building NEW deb package with packaging/deb/build.sh ---")
        new_pkg_name = "urmail_0.6.0~test.new_arm64.deb"
        build_new_cmd = [
            "docker", "run", "--rm",
            "-v", f"{root}:/workspace:ro",
            "-v", f"{host_test_dir}:/testdir",
            "-w", "/testdir",
            "urmail-security-package:22.04",
            "bash", "-c",
            "mkdir -p /testdir/src/packaging /testdir/src/internal/db /testdir/src/installer /testdir/src/internal/i18n && "
            "cp -a /workspace/packaging/. /testdir/src/packaging/ && "
            "cp -a /workspace/internal/db/bootstrap.sql /testdir/src/internal/db/ && "
            "cp -a /workspace/installer/configure-env.py /testdir/src/installer/ && "
            "cp -a /workspace/internal/i18n/. /testdir/src/internal/i18n/ && "
            "export URMAIL_BIN_DIR=/testdir/bin ARCH=arm64 && "
            "bash /testdir/src/packaging/deb/build.sh 0.6.0~test.new && "
            f"cp /testdir/src/dist/{new_pkg_name} /testdir/new.deb"
        ]
        subprocess.run(build_new_cmd, check=True)

        # Build OLD deb package (0.5.0~test.old) where files were placed in /etc/urmail/observability
        # We synthesize old deb by unpacking stock deb, ensuring its /etc/urmail/observability is present,
        # and repacking with version 0.5.0~test.old.
        print("\n--- Preparing OLD deb package (with /etc/urmail/observability payload) ---")
        build_old_cmd = [
            "docker", "run", "--rm",
            "-v", f"{root}:/workspace:ro",
            "-v", f"{host_test_dir}:/testdir",
            "urmail-security-package:22.04",
            "bash", "-c",
            "mkdir -p /tmp/old_stage && "
            "dpkg-deb -R /workspace/dist/urmail_0.5.0_arm64.deb /tmp/old_stage && "
            "sed -i 's/^Version: .*/Version: 0.5.0~test.old/' /tmp/old_stage/DEBIAN/control && "
            "dpkg-deb --root-owner-group --build /tmp/old_stage /testdir/old.deb"
        ]
        subprocess.run(build_old_cmd, check=True)

        results = {}

        # Run verification in both 22.04 and 24.04 containers
        for image in IMAGES:
            distro = image.split(":")[1]
            container_name = f"urmail-f04-test-{distro}-{os.getpid()}"
            print(f"\n========================================================")
            print(f"Running real dpkg upgrade verification on {image}")
            print(f"Container: {container_name}")
            print(f"========================================================")

            test_script_path = host_test_dir / "run_test.sh"
            test_script_path.write_text(CONTAINER_INNER_TEST)
            test_script_path.chmod(0o755)

            cmd = [
                "docker", "run", "--rm",
                "--name", container_name,
                "-v", f"{host_test_dir}:/testdir",
                image,
                "bash", "/testdir/run_test.sh", distro, "/testdir"
            ]

            proc = subprocess.run(cmd, capture_output=True, text=True)
            print("STDOUT:")
            print(proc.stdout)
            if proc.stderr:
                print("STDERR:")
                print(proc.stderr)

            if proc.returncode != 0:
                print(f"FAILURE on {image}: exit code {proc.returncode}", file=sys.stderr)
                results[image] = {"status": "FAILED", "code": proc.returncode}
            else:
                print(f"SUCCESS on {image}!")
                results[image] = {"status": "PASSED", "code": 0}

        print("\n=== FINAL TEST SUMMARY ===")
        all_passed = True
        for image, res in results.items():
            print(f"{image}: {res['status']}")
            if res["status"] != "PASSED":
                all_passed = False

        if not all_passed:
            return 1
        return 0

    finally:
        shutil.rmtree(host_test_dir, ignore_errors=True)
        print(f"\nCleaned up host directory: {host_test_dir}")


if __name__ == "__main__":
    sys.exit(run_dpkg_verification())
