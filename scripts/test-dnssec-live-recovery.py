#!/usr/bin/env python3
"""Opt-in F02 daemon integration. No host mounts, published ports or --privileged.

Drops all capabilities except CHOWN/DAC_OVERRIDE for container-only fixture files.

Runs real BIND + NSD on container loopback :1053; tests ApplyManager rollback
and a SIGKILL'd ApplyManager subprocess with journal recovery in a fresh process.
Not a full agent/systemd/PostgreSQL acceptance. See the Go fixture scope comment.
"""
import argparse
import os
import pathlib
import subprocess
import tempfile
import uuid


def run(*args, **kwargs):
    print("+", " ".join(str(a) for a in args), flush=True)
    return subprocess.run(args, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="urmail-dnsfix-runtime:24.04")
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[1]
    name = "urmail-f02-live-" + uuid.uuid4().hex[:12]
    architecture = subprocess.check_output(
        ["docker", "image", "inspect", args.image, "--format", "{{.Architecture}}"],
        text=True,
    ).strip()
    if architecture not in ("arm64", "amd64"):
        raise SystemExit("unsupported image architecture: " + architecture)
    env = dict(os.environ, GOOS="linux", GOARCH=architecture, CGO_ENABLED="0")
    with tempfile.TemporaryDirectory(prefix="urmail-f02-build-") as temporary:
        binary = str(pathlib.Path(temporary) / "agentd-live.test")
        run("go", "test", "-c", "-o", binary, "./internal/agentd", cwd=root, env=env)
        created = False
        try:
            run("docker", "run", "-d", "--rm", "--name", name,
                "--network", "none", "--cap-drop", "ALL",
                "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE",
                "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh",
                args.image, "-c", "exec tail -f /dev/null")
            created = True
            run("docker", "exec", name, "/bin/sh", "-ec",
                "for tool in nsd nsd-control dig dnssec-keygen dnssec-signzone "
                "dnssec-verify dnssec-dsfromkey nsd-checkzone nsd-checkconf; "
                'do command -v "$tool"; done; '
                "nsd -v; dig -v")
            run("docker", "cp", binary, name + ":/tmp/agentd-live.test")
            run("docker", "exec", "-e", "URMAIL_REAL_DNSSEC=1",
                "-e", "URMAIL_LIVE_DNSSEC=1",
                "-e", "URMAIL_DNSSEC_FIXTURE_DIR=/tmp/f02-live",
                name, "/tmp/agentd-live.test", "-test.v",
                "-test.run=^TestLiveDNSSECRollbackAndSIGKILLRecovery$",
                "-test.timeout=180s")
        finally:
            if created:
                run("docker", "stop", "--timeout", "1", name)
    print("PASS: real daemon rollback + SIGKILL journal recovery; owned container removed")


if __name__ == "__main__":
    main()
