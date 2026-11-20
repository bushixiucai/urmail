#!/usr/bin/env python3
"""S3 probe: the 0142 recall review state machine as ported into
migrations_sqlite/0001_init.sql (recall_state_guard_insert/update,
recall_message_identity_guard_*) plus the 0002 audit triggers.

Why this probe exists: PHASE1-S2-GUARD-PORT-AUDIT called the SQLite port a
"stale 0025 port" with zero 0142 coverage. The triggers were rewritten, but
nothing exercised them — the only behavioural tests for the review flow run
against PostgreSQL (internal/db/v3_boundaries_test.go). This probe runs the
real SQLite schema and asserts both the accepted path (preview → pending →
local_recalled) and every rejection branch the audit listed.

Run: python3 scripts/sqlite/probes/s3_recall_guard_probe.py
"""
import json
import pathlib
import sqlite3
import time

ROOT = pathlib.Path(__file__).resolve().parents[3]
# The triggers compare against strftime('%s','now'); a frozen fixture timestamp
# would land outside the 301s preview window.
NOW = int(time.time() * 1_000_000)
DAY = 86_400 * 1_000_000

TENANT = 7
MAILBOX = 11
ENTRY = 21
ACTOR = 3
REQUESTER = 3
MESSAGE = "<recall-probe@example.test>"
GUID = "1234567890abcdef1234567890abcdef"
MESSAGE_GUID = "recall-probe@example.test"
UIDVALIDITY = 42
UID = 5


def fresh():
    con = sqlite3.connect(":memory:")
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA foreign_keys=ON")
    for f in ["0001_init.sql", "0002_write_side_effects.sql"]:
        con.executescript((ROOT / "migrations_sqlite" / f).read_text())
    con.execute("INSERT INTO brands(id,slug,name) VALUES(1,'probe','Probe')")
    con.execute(
        "INSERT INTO users(id,email,password_hash,status) VALUES(?,?,?,'active')",
        (ACTOR, "actor@example.test", "x"),
    )
    con.execute(
        "INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(?,1,'Probe','probe','active',?)",
        (TENANT, ACTOR),
    )
    con.execute(
        """INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state)
           VALUES(31,?,?,'active','tok',?,'pending')""",
        (TENANT, "example.test", NOW),
    )
    # domain_provisioning_insert_pending forces a new domain to 'pending' when
    # dns_explicit=0; the probe needs a provisioned domain, and only INSERT is
    # reset, so the explicit UPDATE is the supported way to model "ready".
    con.execute("UPDATE domains SET provisioning_state='ready' WHERE id=31")
    con.execute(
        """INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status)
           VALUES(?,?,31,'actor','actor@example.test','x',-1,'active')""",
        (MAILBOX, TENANT),
    )
    con.execute(
        """INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at)
           VALUES(?,?,?,?,?,?)""",
        (ENTRY, TENANT, MAILBOX, MESSAGE, "/probe/message.eml", NOW - DAY),
    )
    con.commit()
    return con


def snapshot(**override):
    doc = {
        "tenant_id": TENANT,
        "entry_id": ENTRY,
        "mailbox_id": MAILBOX,
        "message_id": MESSAGE,
        "actor_id": ACTOR,
        "address": "actor@example.test",
        # 0142 selects copies by guid:uid_validity:uid, while 0145 validates
        # message_guid (the canonical RFC message identity). Both fields are
        # required: dropping either makes one guard trip.
        "copies": [
            {
                "guid": GUID,
                "message_guid": MESSAGE_GUID,
                "uid_validity": UIDVALIDITY,
                "uid": UID,
                "folder": "INBOX",
            }
        ],
    }
    doc.update(override)
    return json.dumps(doc)


def insert_preview(con, **override):
    row = {
        "tenant_id": TENANT,
        "archive_entry_id": ENTRY,
        "mailbox_id": MAILBOX,
        "message_id": MESSAGE,
        "requested_by": REQUESTER,
        "status": "preview",
        "preview_actor_id": ACTOR,
        "preview_token": "tok-1",
        "preview_digest": "d" * 64,
        "preview_snapshot": snapshot(),
        "preview_expires_at": NOW + 60_000_000,
        "attempt_epoch": 1,
    }
    row.update(override)
    cols = ",".join(row)
    marks = ",".join("?" for _ in row)
    con.execute(f"INSERT INTO recall_requests({cols}) VALUES({marks})", tuple(row.values()))


def transition(con, **set_to):
    sets = ",".join(f"{k}=?" for k in set_to)
    con.execute(f"UPDATE recall_requests SET {sets} WHERE tenant_id=? AND archive_entry_id=?",
                tuple(set_to.values()) + (TENANT, ENTRY))


def failure(label, fn, want):
    """fn must abort with the given RAISE message."""
    con = fresh()
    try:
        fn(con)
    except sqlite3.IntegrityError as e:
        if want in str(e):
            print(f"PASS {label}")
        else:
            failures.append(label)
            print(f"FAIL {label}: aborted with {e!r}, wanted {want!r}")
        return
    failures.append(label)
    print(f"FAIL {label}: accepted, wanted abort {want!r}")


def success(label, fn):
    con = fresh()
    try:
        fn(con)
    except Exception as e:  # noqa: BLE001 - the probe reports the real reason
        failures.append(label)
        print(f"FAIL {label}: {e!r}")
        return
    print(f"PASS {label}")


failures = []


def check(label, got, want):
    if got == want:
        print(f"PASS {label}")
    else:
        failures.append(label)
        print(f"FAIL {label}: got {got!r} want {want!r}")


def main():
    # --- accepted path -----------------------------------------------------
    success("preview insert with proof", lambda c: (insert_preview(c), c.commit()))
    success(
        "preview -> pending with selection",
        lambda c: (
            insert_preview(c),
            transition(
                c,
                status="pending",
                confirmed_at=NOW,
                selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"]),
            ),
            c.commit(),
        ),
    )
    success(
        "pending -> local_recalled with completion proof",
        lambda c: (
            insert_preview(c),
            transition(
                c,
                status="pending",
                confirmed_at=NOW,
                selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"]),
            ),
            transition(c, status="local_recalled", completed_at=NOW, local_result=json.dumps({"items": []})),
            c.commit(),
        ),
    )

    # --- INSERT rejected ---------------------------------------------------
    failure("direct pending insert", lambda c: insert_preview(c, status="pending"),
            "RECALL_PREVIEW_REQUIRED")
    failure("direct delivered insert", lambda c: insert_preview(c, status="delivered"),
            "RECALL_PREVIEW_REQUIRED")
    failure("preview insert without token",
            lambda c: insert_preview(c, preview_token=None), "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert with expired proof",
            lambda c: insert_preview(c, preview_expires_at=NOW - DAY), "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert with proof window beyond 301s",
            lambda c: insert_preview(c, preview_expires_at=NOW + 302_000_000),
            "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert without epoch bump",
            lambda c: insert_preview(c, attempt_epoch=0), "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert with non-object snapshot",
            lambda c: insert_preview(c, preview_snapshot="[]"), "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert with existing confirmation",
            lambda c: insert_preview(c, confirmed_at=NOW), "RECALL_PREVIEW_PROOF_REQUIRED")
    failure("preview insert with foreign tenant in snapshot",
            lambda c: insert_preview(c, preview_snapshot=snapshot(tenant_id=TENANT + 1)),
            "RECALL_PREVIEW_TARGET_MISMATCH")
    failure("preview insert with foreign mailbox in snapshot",
            lambda c: insert_preview(c, preview_snapshot=snapshot(mailbox_id=MAILBOX + 1)),
            "RECALL_PREVIEW_TARGET_MISMATCH")
    failure("preview insert without copies array",
            lambda c: insert_preview(c, preview_snapshot=snapshot(copies={})),
            "RECALL_PREVIEW_TARGET_MISMATCH")
    failure("preview insert with mismatched archive target",
            lambda c: insert_preview(c, mailbox_id=MAILBOX + 99),
            "RECALL_PREVIEW_TARGET_MISMATCH")
    failure("preview insert with non-text copy guid",
            lambda c: insert_preview(c, preview_snapshot=snapshot(copies=[{"message_guid": 5}])),
            "RECALL_IMMUTABLE_MESSAGE_IDENTITY_REQUIRED")
    failure("preview insert with illegal guid charset",
            lambda c: insert_preview(c, preview_snapshot=snapshot(copies=[{"message_guid": "bad guid!"}])),
            "RECALL_IMMUTABLE_MESSAGE_IDENTITY_REQUIRED")

    # --- transitions rejected ---------------------------------------------
    failure(
        "confirm without confirmed_at",
        lambda c: (insert_preview(c), transition(c, status="pending",
                                               confirmed_at=None)),
        "RECALL_CONFIRMATION_PROOF_REQUIRED",
    )
    failure(
        "confirm without selection",
        lambda c: (insert_preview(c), transition(c, status="pending", confirmed_at=NOW)),
        "RECALL_CONFIRMATION_PROOF_REQUIRED",
    )
    failure(
        "confirm with unknown copy key",
        lambda c: (
            insert_preview(c),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID + 1}"])),
        ),
        "RECALL_COPY_SELECTION_INVALID",
    )
    failure(
        "confirm with duplicated copy key",
        lambda c: (
            insert_preview(c),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"] * 2)),
        ),
        "RECALL_COPY_SELECTION_INVALID",
    )
    failure(
        "confirm while a copy is under legal hold",
        lambda c: (
            insert_preview(c),
            c.execute("UPDATE archive_entries SET legal_hold=1 WHERE id=?", (ENTRY,)),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"])),
        ),
        "RECALL_HELD_COPY",
    )
    failure(
        "local_recalled without completion proof",
        lambda c: (
            insert_preview(c),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"])),
            transition(c, status="local_recalled"),
        ),
        "RECALL_VERIFIED_LOCAL_PROOF_REQUIRED",
    )
    failure(
        "pending rolled back to preview",
        lambda c: (
            insert_preview(c),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"])),
            transition(c, status="preview", confirmed_at=None),
        ),
        "RECALL_IN_PROGRESS",
    )
    failure(
        "unlisted transition pending -> delivered",
        lambda c: (
            insert_preview(c),
            transition(c, status="pending", confirmed_at=NOW,
                       selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"])),
            transition(c, status="delivered"),
        ),
        "RECALL_STATE_TRANSITION_REJECTED",
    )
    failure(
        "identity mutation on update",
        lambda c: (insert_preview(c), transition(c, message_id="<other@example.test>")),
        "RECALL_IDENTITY_IMMUTABLE",
    )

    # --- audit triggers ----------------------------------------------------
    con = fresh()
    insert_preview(con)
    transition(
        con,
        status="pending",
        confirmed_at=NOW,
        selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"]),
    )
    transition(con, status="local_recalled", completed_at=NOW)
    con.commit()
    rows = con.execute(
        "SELECT action FROM audit_logs WHERE tenant_id=? AND target_type='recall'", (TENANT,)
    ).fetchall()
    check("review completion audit row", [r["action"] for r in rows],
          ["compliance.recall.review_complete"])
    detail = con.execute(
        "SELECT detail FROM audit_logs WHERE target_type='recall' AND action='compliance.recall.review_complete'"
    ).fetchone()["detail"]
    check("audit detail epoch", json.loads(detail)["epoch"], 1)
    check("audit detail selection", json.loads(detail)["selection"],
          json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"]))

    # The 0002 audit triggers split on preview_actor_id: the review variant when
    # it is set, the "_direct" variant when it is not. The insert guard does not
    # require an actor (that obligation is app_pending), so the direct branch is
    # reachable and is asserted rather than assumed dead.
    con = fresh()
    insert_preview(con, preview_actor_id=None)
    transition(
        con,
        status="pending",
        confirmed_at=NOW,
        selected_copy_keys=json.dumps([f"{GUID}:{UIDVALIDITY}:{UID}"]),
    )
    transition(con, status="local_recalled", completed_at=NOW)
    con.commit()
    direct = con.execute(
        "SELECT action FROM audit_logs WHERE target_type='recall' AND actor_user_id=?",
        (REQUESTER,),
    ).fetchall()
    check("direct completion audit row", [r["action"] for r in direct],
          ["compliance.recall.complete"])

    print("fails:", failures)


if __name__ == "__main__":
    main()