#!/usr/bin/env python3
"""Redact a real production .eml into a corpus fixture.

Keeps the MIME structure and the body text intact while removing
identifying data:

- traceability headers are dropped (Received, DKIM-Signature, ARC-*,
  Authentication-Results, Return-Path);
- every domain name, in address headers and in decoded text parts, is
  replaced with sha256(domain)[:10].redacted (stable, so the same domain
  always maps to the same placeholder);
- quoted-printable/base64 text parts are decoded, redacted, and
  re-encoded; binary parts are left untouched.

Usage:
    python3 scripts/corpus-redact.py path/to/real.eml \
        > internal/webmail/testdata/corpus/name.eml

The output is deterministic for a given input.
"""

from __future__ import annotations

import email
import email.utils
import hashlib
import re
import sys

DROP_HEADERS = {
    "received",
    "dkim-signature",
    "arc-authentication-results",
    "arc-message-signature",
    "arc-seal",
    "authentication-results",
    "return-path",
}

# A dotted domain (labels only, no scheme). Applies inside header values
# and inside decoded text payloads.
DOMAIN_RE = re.compile(r"(?<![A-Za-z0-9-])((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,24})(?![A-Za-z0-9-])")
# URL scheme + domain: keep the scheme so links stay recognizable.
URL_RE = re.compile(r"\b(https?://)((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,24})")

# Very common TLDs stay, because hashing them would destroy readability of
# the layout under test (e.g. "example.test").
KEEP_DOMAINS = {"example.test", "example.com", "example.org"}

_domain_cache: dict[str, str] = {}


def redact_domain(domain: str) -> str:
    if domain.lower() in KEEP_DOMAINS:
        return domain
    hit = _domain_cache.get(domain)
    if hit is None:
        hit = hashlib.sha256(domain.lower().encode()).hexdigest()[:10] + ".redacted"
        _domain_cache[domain] = hit
    return hit


def redact_text(text: str) -> str:
    text = URL_RE.sub(lambda m: m.group(1) + redact_domain(m.group(2)), text)
    text = DOMAIN_RE.sub(lambda m: redact_domain(m.group(1)), text)
    return text


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    with open(sys.argv[1], "rb") as f:
        raw = f.read()

    msg = email.message_from_bytes(raw)

    # Drop traceability headers and redact the remaining header values.
    # The domain regex only touches the domain labels, so address structs
    # (angle brackets, display names) survive intact.
    for key in [k for k in msg.keys() if k.lower() in DROP_HEADERS]:
        del msg[key]
    for key in [k for k in msg.keys()]:
        value = msg[key]
        if value is not None:
            msg.replace_header(key, redact_text(value))

    # Redact decoded text parts; keep binary parts as-is. Re-encoding as
    # base64 via set_payload(charset=...) keeps serialization valid for any
    # content, at the cost of changing the transfer encoding of text parts.
    for part in msg.walk():
        if part.get_content_maintype() == "text":
            payload = part.get_payload(decode=True)
            if payload is None:
                continue
            charset = part.get_content_charset() or "utf-8"
            try:
                text = payload.decode(charset, errors="replace")
            except LookupError:
                text = payload.decode("utf-8", errors="replace")
            for key in ("content-transfer-encoding", "content-disposition"):
                del part[key]
            part.set_payload(redact_text(text), charset="utf-8")

    sys.stdout.buffer.write(msg.as_bytes())
    return 0


if __name__ == "__main__":
    sys.exit(main())
