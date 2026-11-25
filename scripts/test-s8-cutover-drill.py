#!/usr/bin/env python3
"""Offline S8 cutover & rollback drill (ARCH-PLAN §3 Phase 4 / V3.2 §7 S8).

Exercises without touching production:
  1. Dual-stack disk budget preflight (pass + fail-closed shortfall).
  2. Cutover drill: canary (450 4xx SMTP deferral) -> release -> queued mail
     retried and delivered with zero loss.
  3. Rollback drill: in-window zero-loss rollback succeeds; post-release
     zero-loss rollback is refused; break-glass reverse export succeeds.
"""

import json
import os
import pathlib
import shutil
import smtplib
import socketserver
import sqlite3
import subprocess
import sys
import tempfile
import threading


def run_cli(cli, args):
    p = subprocess.run([cli] + args, text=True, capture_output=True)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


class CanaryDeferHandler(socketserver.StreamRequestHandler):
    """Minimal SMTP responder returning 450 4.3.2 during canary window."""

    def handle(self):
        self.wfile.write(b"220 canary.local ESMTP\r\n")
        while True:
            line = self.rfile.readline()
            if not line:
                break
            cmd = line.decode("ascii", errors="ignore").strip().upper()
            if cmd.startswith("EHLO") or cmd.startswith("HELO"):
                self.wfile.write(b"250-canary.local\r\n250 SIZE 10485760\r\n")
            elif cmd.startswith("MAIL FROM"):
                self.wfile.write(b"250 2.1.0 Ok\r\n")
            elif cmd.startswith("RCPT TO"):
                self.wfile.write(b"450 4.3.2 cutover canary in progress; please retry\r\n")
            elif cmd.startswith("QUIT"):
                self.wfile.write(b"221 Bye\r\n")
                break
            else:
                self.wfile.write(b"250 Ok\r\n")


class PostReleaseAcceptHandler(socketserver.StreamRequestHandler):
    """Minimal SMTP responder accepting retried mail after cutover release."""

    received = []

    def handle(self):
        self.wfile.write(b"220 released.local ESMTP\r\n")
        in_data = False
        buf = []
        while True:
            line = self.rfile.readline()
            if not line:
                break
            if in_data:
                if line == b".\r\n":
                    self.received.append(b"".join(buf).decode("utf-8", errors="replace"))
                    in_data = False
                    buf.clear()
                    self.wfile.write(b"250 2.0.0 Ok: queued\r\n")
                else:
                    buf.append(line)
                continue
            cmd = line.decode("ascii", errors="ignore").strip().upper()
            if cmd.startswith("EHLO") or cmd.startswith("HELO"):
                self.wfile.write(b"250-released.local\r\n250 SIZE 10485760\r\n")
            elif cmd.startswith("DATA"):
                in_data = True
                self.wfile.write(b"354 End data with <CR><LF>.<CR><LF>\r\n")
            elif cmd.startswith("QUIT"):
                self.wfile.write(b"221 Bye\r\n")
                break
            else:
                self.wfile.write(b"250 Ok\r\n")


def main():
    repo_root = pathlib.Path(__file__).resolve().parent.parent
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="urmail-s8-drill-"))
    try:
        cli = str(tmp / "urmailctl")
        env = dict(os.environ)
        env.setdefault("GOPROXY", "off")
        subprocess.run(
            ["go", "build", "-o", cli, "./cmd/urmailctl"],
            cwd=str(repo_root),
            env=env,
            check=True,
        )

        # 1. Dual-stack disk budget preflight (PG*1.1 + SQLite*2 + snapshot)
        rc, out, _ = run_cli(cli, [
            "cutover", "disk-budget",
            "--pg-bytes", "1000", "--sqlite-bytes", "500",
            "--snapshot-bytes", "200", "--available-bytes", "2300",
        ])
        assert rc == 0, f"disk-budget pass failed: {out}"
        budget = json.loads(out)
        assert budget["sufficient"] is True and budget["required_bytes"] == 2300
        assert budget["pg_retention_days"] == 14

        rc, _, _ = run_cli(cli, [
            "cutover", "disk-budget",
            "--pg-bytes", "1000", "--sqlite-bytes", "500",
            "--snapshot-bytes", "200", "--available-bytes", "2299",
        ])
        assert rc != 0, "expected insufficient disk budget to fail closed"

        # 2. Cutover drill: canary -> 4xx defer -> release -> zero-loss retry delivery
        state1 = str(tmp / "cutover-drill.json")
        rc, out, err = run_cli(cli, ["cutover", "canary", "--state-file", state1])
        assert rc == 0, f"canary failed: {err}"
        st = json.loads(out)
        assert st["phase"] == "canary" and st["inbound_4xx_reply"].startswith("450 ")

        # Verify SMTP 4xx deferral during canary retains message in sender queue
        queue = ["Subject: S8 Canary Retry Test\r\n\r\nzero-loss body\r\n"]
        with socketserver.TCPServer(("127.0.0.1", 0), CanaryDeferHandler) as srv:
            port = srv.server_address[1]
            t = threading.Thread(target=srv.serve_forever, daemon=True)
            t.start()
            with smtplib.SMTP("127.0.0.1", port, timeout=5) as client:
                client.ehlo("sender.test")
                client.mail("sender@example.test")
                code, resp = client.rcpt("alice@example.test")
                assert code == 450, f"expected 450 deferral during canary, got {code} {resp}"
            srv.shutdown()

        # Release production writes -> zero-loss window closes, queued mail retried
        rc, out, err = run_cli(cli, ["cutover", "release", "--state-file", state1])
        assert rc == 0, f"release failed: {err}"
        st = json.loads(out)
        assert st["phase"] == "released" and st.get("first_prod_write_at")

        PostReleaseAcceptHandler.received.clear()
        with socketserver.TCPServer(("127.0.0.1", 0), PostReleaseAcceptHandler) as srv:
            port = srv.server_address[1]
            t = threading.Thread(target=srv.serve_forever, daemon=True)
            t.start()
            with smtplib.SMTP("127.0.0.1", port, timeout=5) as client:
                for msg in list(queue):
                    client.sendmail("sender@example.test", ["alice@example.test"], msg)
                    queue.remove(msg)
            srv.shutdown()
        assert len(queue) == 0 and len(PostReleaseAcceptHandler.received) == 1, "retried message lost"

        # Post-release zero-loss rollback must be refused
        rc, _, _ = run_cli(cli, ["cutover", "rollback", "--state-file", state1])
        assert rc != 0, "expected post-release zero-loss rollback refusal"

        # Break-glass thin reverse export after window closed
        db_path = str(tmp / "live.sqlite")
        with sqlite3.connect(db_path) as conn:
            conn.execute("PRAGMA journal_mode=WAL")
            conn.execute("PRAGMA user_version = 7")
            conn.execute("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL DEFAULT 0)")
            conn.execute("INSERT INTO schema_migrations(version, dirty) VALUES(1, 0)")
            conn.execute("CREATE TABLE mail_witness(id INTEGER PRIMARY KEY, body TEXT)")
            conn.execute("INSERT INTO mail_witness(id, body) VALUES(1, 'post-release')")
        bg_out = str(tmp / "breakglass.json")
        rc, out, err = run_cli(cli, [
            "cutover", "break-glass", "--sqlite", db_path, "--out", bg_out, "--state-file", state1,
        ])
        assert rc == 0, f"break-glass failed: {err}"
        bg = json.loads(out)
        assert bg["no_sla_commitment"] is True and bg["tables"]["mail_witness"] == 1

        # 3. Rollback drill inside canary window (zero-loss rollback)
        state2 = str(tmp / "rollback-drill.json")
        rc, _, err = run_cli(cli, ["cutover", "canary", "--state-file", state2])
        assert rc == 0, err
        rc, out, err = run_cli(cli, ["cutover", "rollback", "--state-file", state2])
        assert rc == 0, f"in-window rollback failed: {err}"
        rb = json.loads(out)
        assert rb["phase"] == "rolled_back_zero_loss"

        print(json.dumps({
            "drill": "PASS",
            "disk_budget_required": budget["required_bytes"],
            "canary_4xx_code": 450,
            "retried_delivered": len(PostReleaseAcceptHandler.received),
            "in_window_rollback_phase": rb["phase"],
            "break_glass_no_sla": bg["no_sla_commitment"],
        }))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    main()
