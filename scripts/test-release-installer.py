#!/usr/bin/env python3
"""Exercise release downloads on an explicitly disposable Ubuntu installation.

This runs the real installer, including successful package installation. It is
intended for a fresh test VM, never an existing mail server. A temporary
installer copy trusts a per-run fixture key; the production trust key is untouched.
Use --download-only for isolated signature/download checks without installation.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import urllib.parse


def print_failure_output(output, environment):
    """Expose useful CI diagnostics without publishing installer credentials."""
    text = output.decode("utf-8", errors="replace")
    for key, value in environment.items():
        if value and any(word in key for word in ("PASSWORD", "TOKEN", "SECRET")):
            text = text.replace(value, "[REDACTED]")
    text = re.sub(r"(postgres(?:ql)?://[^\s:/]+:)[^\s@]+(@)", r"\1[REDACTED]\2", text)
    text = re.sub(r"(?im)^([^\n=]*(?:password|token|secret)[^\n=]*=).*$", r"\1[REDACTED]", text)
    print("Sanitized installer failure output:", flush=True)
    print("\n".join(text.splitlines()[-100:]), flush=True)


def prepare_signed_fixture(source, directory, manifest, download_only):
    """Sign fixture manifests and replace only a disposable installer's trust key."""
    key = directory / "fixture-signing.key"
    public = directory / "fixture-signing.pub"
    subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key)],
                   check=True, capture_output=True)
    key.chmod(0o600)
    subprocess.run(["openssl", "pkey", "-in", str(key), "-pubout", "-out", str(public)],
                   check=True, capture_output=True)
    original = source.read_text()
    pinned_key = r"-----BEGIN PUBLIC KEY-----\n[A-Za-z0-9+/=\n]+-----END PUBLIC KEY-----"
    installer, replacements = re.subn(pinned_key, lambda _match: public.read_text().rstrip(), original)
    assert replacements == 1, "expected exactly one installer release trust key"
    if download_only:
        # Exercise the actual download and verification block on isolated Linux
        # without installing dependencies, the package, or a database.
        dependency_start = installer.index("# Dependencies\n")
        package_start = installer.index("# Package\n", dependency_start)
        installer = installer[:dependency_start] + installer[package_start:]
        installer = installer[:installer.index('log "installing the package"')]
        installer += 'printf "%s\\n" "Release download contract verified."\n'
    target = directory / "install-fixture.sh"
    target.write_text(installer)
    artifacts = {}
    for mode in ["latest", "fixed", "missing-signature", "bad-signature", "tampered-manifest",
                 "bad-hash", "wrong-arch", "missing-manifest", "missing-package"]:
        value = dict(manifest)
        if mode == "bad-hash":
            value["sha256"] = "0" * 64
        if mode == "wrong-arch":
            value["architecture"] = "amd64" if manifest["architecture"] == "arm64" else "arm64"
        raw = json.dumps(value).encode()
        document = directory / (mode + ".json")
        signature = directory / (mode + ".sig")
        document.write_bytes(raw)
        subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(key), "-rawin",
                        "-in", str(document), "-out", str(signature)], check=True, capture_output=True)
        signed = signature.read_bytes()
        if mode == "bad-signature":
            signed = bytes([signed[0] ^ 1]) + signed[1:]
        if mode == "tampered-manifest":
            value["sha256"] = "1" * 64
            raw = json.dumps(value).encode()
        artifacts[mode] = (raw, signed)
    return target, artifacts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--disposable", action="store_true")
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--download-only", action="store_true",
                        help="test download/signature contracts without dependency or package installation")
    args = parser.parse_args()
    if not args.disposable or os.geteuid() != 0:
        parser.error("requires root and --disposable on a test VM")
    package = args.package.resolve(strict=True)
    arch = subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()
    manifest = {"version": args.version, "architecture": arch,
                "filename": package.name, "sha256": hashlib.sha256(package.read_bytes()).hexdigest()}
    state = {"mode": "latest"}
    fixture = tempfile.TemporaryDirectory(prefix="urmail-release-signing-")
    source = Path(__file__).resolve().parents[1] / "installer/install.sh"
    try:
        installer, artifacts = prepare_signed_fixture(source, Path(fixture.name), manifest, args.download_only)
    except BaseException:
        fixture.cleanup()
        raise

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            route = urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
            prefix = "/releases/latest/download/" if state["mode"] == "latest" else f"/releases/download/v{args.version}/"
            if not route.startswith(prefix):
                self.send_error(404)
                return
            name = route[len(prefix):]
            if name == f"manifest-{arch}.json":
                if state["mode"] == "missing-manifest":
                    self.send_error(404)
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(artifacts[state["mode"]][0])
            elif name == f"manifest-{arch}.json.sig":
                if state["mode"] == "missing-signature":
                    self.send_error(404)
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(artifacts[state["mode"]][1])
            elif name == package.name and state["mode"] != "missing-package":
                self.send_response(200)
                self.end_headers()
                with package.open("rb") as source:
                    shutil.copyfileobj(source, self.wfile)
            else:
                self.send_error(404)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    base = f"http://127.0.0.1:{server.server_port}"
    try:
        logs = tempfile.mkdtemp(prefix="urmail-release-test-")
        print(f"Private installer evidence: {logs}", flush=True)
        for mode, expected in [("missing-signature", "Release manifest signature missing"),
                               ("bad-signature", "Release manifest signature verification failed"),
                               ("tampered-manifest", "Release manifest signature verification failed"),
                               ("bad-hash", "Package checksum mismatch"),
                               ("wrong-arch", "Invalid release manifest"),
                               ("missing-manifest", "404"), ("missing-package", "Release download failed"),
                               ("latest", None), ("fixed", "prepared-held")]:
            state["mode"] = mode
            env = dict(os.environ)
            env.pop("URMAIL_PACKAGE", None)
            env.update(URMAIL_VERSION="latest" if mode == "latest" else "v" + args.version,
                       URMAIL_REPO=base, URMAIL_MIRROR="" if mode == "latest" else base)
            result = subprocess.run(["bash", str(installer)], env=env, capture_output=True, timeout=900)
            output = result.stdout + result.stderr
            Path(logs, mode + ".log").write_bytes(output)
            if expected == "prepared-held":
                # The managed upgrade deliberately stops after preparation:
                # fence held, migrations applied, exit 2 distinguishes it from
                # a completed installation. Phase B release is then the
                # documented operator sequence with the signed readiness
                # receipt, never an automatic release.
                banner = b"UPGRADE PREPARATION & MIGRATIONS APPLIED"
                if result.returncode != 2 or banner not in output:
                    print_failure_output(output, env)
                assert result.returncode == 2, f"{mode}: installer did not stop prepared-held (exit {result.returncode})"
                assert banner in output, f"{mode}: prepared-held banner missing"
                for command in (["urmailctl", "upgrade", "readiness-record"],
                                ["urmailctl", "upgrade", "release"]):
                    phase = subprocess.run(command, env=env, capture_output=True, timeout=900)
                    output = phase.stdout + phase.stderr
                    Path(logs, mode + "-" + command[-1] + ".log").write_bytes(output)
                    if phase.returncode != 0:
                        print_failure_output(output, env)
                    assert phase.returncode == 0, f"{mode}: {command[-1]} failed (exit {phase.returncode})"
                hold = "/var/lib/urmail-backup/upgrade.hold"
                assert not os.path.lexists(hold), f"{mode}: upgrade fence retained after release"
            else:
                if ((expected is None and result.returncode != 0)
                        or (expected is not None and (result.returncode == 0 or expected.encode() not in output))):
                    print_failure_output(output, env)
                if expected is None:
                    assert result.returncode == 0, f"{mode}: installer failed (exit {result.returncode})"
                else:
                    assert result.returncode != 0 and expected.encode() in output, f"{mode}: unexpected result"
            print(f"PASS: release installer {mode}", flush=True)
        shutil.rmtree(logs)
    finally:
        server.shutdown()
        server.server_close()
        fixture.cleanup()


if __name__ == "__main__":
    main()
