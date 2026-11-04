#!/usr/bin/env python3
"""F09 actual CLI-process lock integration in a disposable Docker container.

No host system mounts, Docker socket, privileged mode, daemon, database, or mail
service. Only a newly built static CLI is bind-mounted read-only. Container root
creates every script/state fixture inside its own disposable filesystem.
"""
import argparse
import os
from pathlib import Path
import platform
import subprocess
import tempfile

GUEST = r'''
import json, os, pathlib, subprocess, time
P = pathlib.Path
CLI = "/fixture/urmailctl"
root = P("/var/lib/f09-lease")
root.mkdir(mode=0o700, parents=True)
env = dict(os.environ, URMAIL_MAINTENANCE_ROOT=str(root))
stage = P("/tmp/f09-private")
stage.mkdir(mode=0o700)
script = stage / "installer.sh"
ready, finish = root / "ready", root / "finish"
script.write_text("""set -eu
/fixture/urmailctl upgrade check-installer-lease
/fixture/urmailctl upgrade status --json
: > /var/lib/f09-lease/ready
while [ ! -f /var/lib/f09-lease/finish ]; do sleep 0.02; done
/fixture/urmailctl upgrade check-installer-lease
exit 2
""")
script.chmod(0o700)
# Persistent fence fixture: deliberately no real units or automatic release.
(root / "upgrade.hold").write_text(json.dumps({"version":1,"reason":"CLI lease fixture","original_active_units":[]}))
(root / "upgrade.hold").chmod(0o600)

def run(args, *, expected=None, extra_env=None, pass_fds=()):
    child_env = dict(env)
    child_env.update(extra_env or {})
    proc = subprocess.run([CLI] + args, env=child_env, pass_fds=pass_fds,
                          text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=5)
    print(json.dumps({"args":args,"rc":proc.returncode,"output":proc.stdout}), flush=True)
    if expected is not None:
        assert proc.returncode == expected, proc.stdout
    return proc

# Reserve fd3 before Popen allocates its stdout pipe, so deliberate forged
# fd3 injection below never overwrites the parent output capture descriptor.
reserved = os.open("/dev/null", os.O_RDONLY)
assert reserved == 3
parent = subprocess.Popen([CLI,"upgrade","run-installer","--installer",str(script)],
                          env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
try:
    deadline = time.monotonic() + 8
    while not ready.exists():
        if parent.poll() is not None:
            raise AssertionError("installer exited before valid lease: " + parent.communicate()[0])
        assert time.monotonic() < deadline, "installer readiness timed out"
        time.sleep(0.02)
    # Each run creates an ACTUAL independent CLI process without inherited fd3.
    for args in (["upgrade","fence","--timeout","0s"],
                 ["upgrade","release","--timeout","0s"],
                 ["upgrade","run-installer","--installer",str(script)]):
        proc = run(args)
        assert proc.returncode != 0 and "another backup, restore or upgrade operation" in proc.stdout, proc.stdout
    # config prepare has a fixed production lock wait; externally bound it,
    # proving it cannot progress to config/database/service changes while held.
    try:
        subprocess.run([CLI,"config","prepare"],env=env,text=True,
                       stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=0.4)
    except subprocess.TimeoutExpired:
        print(json.dumps({"args":["config","prepare"],"blocked_until_killed":True}),flush=True)
    else:
        raise AssertionError("concurrent config prepare unexpectedly progressed")
    assert (root / "upgrade.hold").exists()
    # Locator strings alone cannot confer a lease or fall back to unguarded IO.
    for selector in ("bogus","3","42"):
        proc = run(["upgrade","fence","--timeout","0s"],extra_env={"URMAIL_UPGRADE_LOCK_FD":selector})
        assert proc.returncode != 0 and ("lease" in proc.stdout or "lock file must be" in proc.stdout), proc.stdout
    # Same inode but a fresh description not owning flock is also not a lease.
    fd = os.open(root / "operation.lock", os.O_RDWR)
    try:
        if fd != 3:
            os.dup2(fd,3)
        proc = run(["upgrade","check-installer-lease"],extra_env={"URMAIL_UPGRADE_LOCK_FD":"3"},pass_fds=(3,))
        assert proc.returncode != 0 and "flock" in proc.stdout, proc.stdout
    finally:
        if fd != 3:
            os.close(3)
        os.close(fd)
    finish.touch()
    output = parent.communicate(timeout=5)[0]
    print(json.dumps({"installer_rc":parent.returncode,"output":output}),flush=True)
    assert parent.returncode == 2, "prepared-held installer status must remain exactly 2"
    assert (root / "upgrade.hold").exists(), "persistent fence was automatically released"
    # No owner remains: unlocked fd3 still must not authenticate itself.
    fd = os.open(root / "operation.lock", os.O_RDWR)
    try:
        if fd != 3:
            os.dup2(fd,3)
        proc = run(["upgrade","check-installer-lease"],extra_env={"URMAIL_UPGRADE_LOCK_FD":"3"},pass_fds=(3,))
        assert proc.returncode != 0 and "not already exclusively held" in proc.stdout, proc.stdout
    finally:
        if fd != 3:
            os.close(3)
        os.close(fd)
    # Verify clean lock ownership can be acquired by another runner after exit.
    script.write_text("set -eu\n/fixture/urmailctl upgrade check-installer-lease\nexit 0\n")
    run(["upgrade","run-installer","--installer",str(script)],expected=0)
    assert (root / "upgrade.hold").exists()
    print("PASS: real root CLI lease, independent-process exclusion, forgery rejection, exit=2, persistent fence retained",flush=True)
finally:
    finish.touch()
    if parent.poll() is None:
        parent.kill()
        parent.communicate(timeout=5)
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="python:3.10-slim")
    parser.add_argument("--arch", choices=("arm64", "amd64"),
                        default="arm64" if platform.machine() in ("arm64", "aarch64") else "amd64")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="urmail-f09-cli-lease-") as temporary:
        binary = Path(temporary) / "urmailctl"
        build_env = dict(os.environ, GOOS="linux", GOARCH=args.arch, CGO_ENABLED="0")
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/urmailctl"], cwd=repo, env=build_env, check=True)
        subprocess.run(["docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL",
                        "--security-opt", "no-new-privileges", "--platform", "linux/" + args.arch,
                        "--mount", "type=bind,src=" + str(binary) + ",dst=/fixture/urmailctl,readonly",
                        "-i", args.image, "python3", "-u", "-"], input=GUEST, text=True, check=True, timeout=60)


if __name__ == "__main__":
    main()
