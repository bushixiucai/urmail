#!/usr/bin/env python3
"""Exercise the remediation API on an explicitly disposable running instance.

Admin credentials are supplied through URMAIL_ACCEPTANCE_TOKEN and
URMAIL_ACCEPTANCE_CSRF, or URMAIL_ACCEPTANCE_EMAIL/PASSWORD/MFA_CODE. Generated
owner accounts and the loopback/test URL may be exported as a 0600 fixture file
for a subsequent browser run. No real payment, DNS write or TLS bypass is used.
"""

import argparse
import json
import os
import secrets
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


class Panel:
    def __init__(self, base, token="", csrf=""):
        self.base = base.rstrip("/")
        self.token = token
        self.csrf = csrf

    def call(self, method, path, body=None, expected=200, key=None):
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        if key:
            headers["Idempotency-Key"] = key
        raw = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            raw = json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, raw, headers, method=method)
        try:
            response = urllib.request.urlopen(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            if response.status != expected:
                # Never print supplied credentials, request bodies or arbitrary
                # server output that might contain a just-created secret.
                raise RuntimeError(f"{method} {path}: status {response.status}, expected {expected}")
            if response.headers.get_content_type() == "application/pdf":
                if not payload.startswith(b"%PDF-"):
                    raise RuntimeError("invoice response is not a PDF")
                return payload
            return json.loads(payload)

    def login(self, email, password, code=None):
        session = self.call("POST", "/api/v1/auth/login", {"email": email, "password": password})
        if session.get("mfa_required"):
            if not code:
                raise RuntimeError("URMAIL_ACCEPTANCE_MFA_CODE is required for this test account")
            session = self.call("POST", "/api/v1/auth/mfa/verify", {
                "mfa_token": session["mfa_token"], "code": code,
            })
        self.token = session["token"]
        self.csrf = session["csrf_token"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--disposable", action="store_true", help="assert this instance uses a disposable test database")
    parser.add_argument("--fixture-out", type=Path, help="write generated test accounts for browser acceptance, mode 0600")
    args = parser.parse_args()
    host = urllib.parse.urlsplit(args.base_url).hostname or ""
    if not args.disposable or not (host in {"localhost", "127.0.0.1", "::1"} or host.endswith((".test", ".example"))):
        parser.error("use --disposable with a loopback, .test or .example test instance")
    admin = Panel(args.base_url, os.environ.get("URMAIL_ACCEPTANCE_TOKEN", ""), os.environ.get("URMAIL_ACCEPTANCE_CSRF", ""))
    if not admin.token or not admin.csrf:
        email = os.environ.get("URMAIL_ACCEPTANCE_EMAIL", "")
        password = os.environ.get("URMAIL_ACCEPTANCE_PASSWORD", "")
        if not email or not password:
            parser.error("supply test admin token/CSRF or email/password through the documented environment variables")
        admin.login(email, password, os.environ.get("URMAIL_ACCEPTANCE_MFA_CODE"))
    suffix = uuid.uuid4().hex[:12]
    code = "smoke-" + suffix
    plan = {"code": code, "name": {"en": "Smoke plan", "zh-CN": "验收套餐"},
            "price_cents": 100, "currency": "CNY", "quota_bytes": 1073741824,
            "mailbox_limit": 5, "domain_limit": 2, "daily_send_limit": 100,
            "features": {}, "active": True}
    admin.call("POST", "/api/v1/platform/plans", plan)
    accounts = []
    panels = []
    for index in range(2):
        email = f"smoke-{suffix}-{index}@example.test"
        password = secrets.token_urlsafe(24)
        tenant = admin.call("POST", "/api/v1/platform/tenants", {
            "name": f"Smoke {suffix} {index}", "slug": f"smoke-{suffix}-{index}",
            "owner_email": email, "owner_password": password,
        }, expected=201)["tenant"]
        owner = Panel(args.base_url)
        owner.login(email, password)
        panels.append(owner)
        accounts.append({"email": email, "password": password, "tenant_id": tenant["id"]})
    owner, other = panels
    owner.call("GET", "/api/v1/tenants/current")
    owner.call("PATCH", "/api/v1/tenants/current", {"settings": {"locale": "en"}})
    owner.call("POST", "/api/v1/platform/brand-hosts", {
        "hostname": f"unauthorized-{suffix}.example.test", "brand_id": 1,
    }, expected=403)
    order = owner.call("POST", "/api/v1/billing/checkout", {"plan_code": code}, expected=201, key=code)
    if order["payment_method"] != "manual":
        raise RuntimeError("this smoke requires payment providers disabled; no real hosted checkout is followed")
    retry = owner.call("POST", "/api/v1/billing/checkout", {"plan_code": code}, expected=201, key=code)
    if retry["order"]["id"] != order["order"]["id"]:
        raise RuntimeError("idempotency retry created another order")
    settlement = f'/api/v1/platform/orders/{order["order"]["id"]}/paid'
    admin.call("POST", settlement)
    admin.call("POST", settlement)
    invoices = owner.call("GET", "/api/v1/billing/invoices")["invoices"]
    if len(invoices) != 1:
        raise RuntimeError("settlement was not exactly once")
    invoice = f'/api/v1/billing/invoices/{invoices[0]["id"]}/pdf'
    owner.call("GET", invoice)
    other.call("GET", invoice, expected=404)
    admin.call("DELETE", "/api/v1/platform/plans/" + code, expected=409)
    plan["active"] = False
    admin.call("POST", "/api/v1/platform/plans", plan)
    owner.call("POST", "/api/v1/billing/checkout", {"plan_code": code}, expected=404, key=code + "-disabled")
    if args.fixture_out:
        raw = json.dumps({"base_url": args.base_url, "accounts": accounts, "plan_code": code}, indent=2).encode()
        fd = os.open(args.fixture_out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as output:
            output.write(raw)
    print("PASS: real HTTP settings, manual checkout retry/settlement, tenant PDF isolation, inactive plan and role boundaries")
    if args.fixture_out:
        print("Generated browser accounts saved to the requested private fixture file")


if __name__ == "__main__":
    main()
