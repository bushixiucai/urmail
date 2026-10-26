#!/usr/bin/env python3
"""Exercise release downloads on an explicitly disposable Ubuntu installation.

This runs the real installer, including successful package installation. It is
intended for a fresh test VM, never an existing mail server.
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--disposable", action="store_true")
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    if not args.disposable or os.geteuid() != 0:
        parser.error("requires root and --disposable on a test VM")
    package = args.package.resolve(strict=True)
    arch = subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()
    manifest = {"version": args.version, "architecture": arch,
                "filename": package.name, "sha256": hashlib.sha256(package.read_bytes()).hexdigest()}
    state = {"mode": "latest"}

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
                value = dict(manifest)
                if state["mode"] == "bad-hash":
                    value["sha256"] = "0" * 64
                if state["mode"] == "wrong-arch":
                    value["architecture"] = "amd64" if arch == "arm64" else "arm64"
                raw = json.dumps(value).encode()
                self.send_response(200)
                self.end_headers()
                self.wfile.write(raw)
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
    installer = Path(__file__).resolve().parents[1] / "installer/install.sh"
    try:
        logs = tempfile.mkdtemp(prefix="urmail-release-test-")
        print(f"Private installer evidence: {logs}", flush=True)
        for mode, expected in [("bad-hash", "Package checksum mismatch"),
                               ("wrong-arch", "Invalid release manifest"),
                               ("missing-manifest", "404"), ("missing-package", "Release download failed"),
                               ("latest", None), ("fixed", None)]:
            state["mode"] = mode
            env = dict(os.environ)
            env.pop("URMAIL_PACKAGE", None)
            env.update(URMAIL_VERSION="latest" if mode == "latest" else "v" + args.version,
                       URMAIL_REPO=base, URMAIL_MIRROR="" if mode == "latest" else base)
            result = subprocess.run(["bash", str(installer)], env=env, capture_output=True, timeout=900)
            output = result.stdout + result.stderr
            Path(logs, mode + ".log").write_bytes(output)
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


if __name__ == "__main__":
    main()
