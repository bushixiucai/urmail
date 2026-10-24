#!/usr/bin/env python3
"""Exercise only an isolated, loopback Alertmanager receiver; never production."""
import datetime
import http.server
import json
import pathlib
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

statuses = []

class Receiver(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        statuses.append(body["status"])
        self.send_response(200)
        self.end_headers()
    def log_message(self, *_):
        pass

server = http.server.HTTPServer(("127.0.0.1", 9099), Receiver)
threading.Thread(target=server.serve_forever, daemon=True).start()
with tempfile.TemporaryDirectory(prefix="urmail-observability-") as root:
    config = pathlib.Path(root) / "alertmanager.yml"
    config.write_text("""route:
  receiver: local-test
  group_by: [alertname, kind]
  group_wait: 1s
  group_interval: 1s
  repeat_interval: 1h
receivers:
  - name: local-test
    webhook_configs:
      - url: http://127.0.0.1:9099/alerts
        send_resolved: true
""")
    process = subprocess.Popen(["/usr/bin/prometheus-alertmanager", f"--config.file={config}", "--web.listen-address=127.0.0.1:9093", "--cluster.listen-address=", f"--storage.path={root}/data"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen("http://127.0.0.1:9093/-/ready", timeout=1).close()
                break
            except Exception:
                time.sleep(0.1)
        now = datetime.datetime.now(datetime.timezone.utc)
        stamp = lambda value: value.isoformat().replace("+00:00", "Z")
        alert = {"labels": {"alertname": "UrmailReview", "kind": "telemetry_queue_pressure"}, "startsAt": stamp(now - datetime.timedelta(seconds=10)), "endsAt": stamp(now + datetime.timedelta(minutes=5)), "annotations": {"summary": "Local fault injection; no customer data"}}
        def submit():
            request = urllib.request.Request("http://127.0.0.1:9093/api/v2/alerts", data=json.dumps([alert]).encode(), headers={"Content-Type": "application/json"})
            urllib.request.urlopen(request, timeout=3).close()
        def await_status(status):
            for _ in range(150):
                if status in statuses:
                    return
                time.sleep(0.1)
            raise RuntimeError(f"receiver missing {status}: {statuses}")
        submit()
        await_status("firing")
        alert["endsAt"] = stamp(datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=1))
        submit()
        await_status("resolved")
        print(json.dumps({"scope": "isolated-loopback-alertmanager", "received": statuses, "production_receiver": "NOT_TESTED"}))
    finally:
        process.terminate()
        process.wait(timeout=10)
        server.shutdown()
