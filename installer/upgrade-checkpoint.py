#!/usr/bin/env python3
"""Root-only, fenced pre-upgrade checkpoint (Python stdlib only).

Run after writers are fenced/stopped and database.dump has finished, before apt:
  python3 /usr/share/urmail/upgrade-checkpoint.py create "$BACKUP_DIR"
  python3 /usr/share/urmail/upgrade-checkpoint.py verify "$BACKUP_DIR"

BACKUP_DIR must already be root-owned 0700 under /root/urmail-upgrade-backups.
The caller owns the operation lock and must ensure no writers: this program does
not stop services or prove database consistency. Verification requires the SAME
live upgrade hold and receipt; preserve them until recovery is accepted.

This is NOT a complete OS/package rollback: old executable bytes and dpkg version
metadata are saved, not old .debs, maintainer scripts, dependencies or all package
files. PostgreSQL physical data, mail/archive payloads and the rest of the system
are deliberately excluded. No automatic extraction/restore is provided. Protect
this directory as secret material. SHA256 detects corruption, not a hostile root.

After a rollback restore, release requires the full re-fence sequence:
  urmailctl upgrade fence → apply migrations (upgrade resume) →
  urmailctl upgrade readiness-record → urmailctl upgrade release.
A legacy v1 fence cannot produce a signed readiness receipt; re-fence first.
Manifests are v2: legacy v1 manifests fail verify closed and must be recreated.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import sys
import tarfile
import time

BASE = "/root/urmail-upgrade-backups"
MAINT = "/var/lib/urmail-backup"
# Explicit managed roots, including actual generator/state paths (not all /etc).
ROOTS = (
    "/etc/urmail", "/etc/postfix/urmail", "/etc/postfix/main.cf",
    "/etc/postfix/master.cf", "/etc/postfix/aliases", "/etc/postfix/aliases.db",
    "/etc/dovecot/urmail", "/etc/dovecot/conf.d/urmail",
    "/etc/dovecot/conf.d/10-auth.conf", "/etc/dovecot/conf.d/urmail-global-acls",
    "/etc/dovecot/urmail-spam", "/etc/nsd/urmail", "/etc/nsd/nsd.conf",
    "/etc/nsd/nsd.conf.d", "/etc/nsd/zones", "/etc/nginx/urmail",
    "/etc/nginx/nginx.conf", "/etc/nginx/conf.d/urmail",
    "/etc/nginx/conf.d/urmail-brand-hosts.conf", "/etc/opendkim",
    "/etc/opendkim.conf", "/etc/sieve/urmail", "/var/lib/urmail-agent",
    "/var/lib/urmail-dnssec",
    # Secondary transfer cache is NSD-owned/untrusted and not authoritative primary state.
    "/var/lib/urmail-acme",
    "/var/lib/urmail-backup/applied_manifest.json",
    "/usr/bin/urmailctl", "/usr/bin/urmaild", "/usr/bin/urmaild-agent",
    "/usr/bin/urmail-agent", "/usr/bin/urmail-archive", "/usr/lib/urmail/archive-pipe",
)
BINARIES = ("/usr/bin/urmailctl", "/usr/bin/urmaild", "/usr/bin/urmaild-agent",
            "/usr/lib/urmail/archive-pipe")
ARTIFACTS = {"database.dump", "urmaild.env", "agent.env", "master.key", "agent-master.key",
             "state.tar", "packages.json", "upgrade.hold", "upgrade_receipt.json"}

# Quiescence proof set: every writer/activation unit from the Go fence list
# (DefaultWriterUnits mirror) plus the Postfix template that guards other
# instances. All must be inactive before and after a checkpoint capture.
QUIESCENCE_UNITS = (
    "dovecot.socket",
    "urmail-backup-auto.timer",
    "urmail-backup-auto.service",
    "urmail-cert-renew.timer",
    "urmail-usage-sync.timer",
    "urmail-cert-renew.service",
    "urmail-usage-sync.service",
    "urmail-prometheus.service",
    "urmail-alertmanager.service",
    "nginx.service",
    "php8.1-fpm.service",
    "php8.2-fpm.service",
    "php8.3-fpm.service",
    "php8.4-fpm.service",
    "php8.5-fpm.service",
    "php8.6-fpm.service",
    "postfix.service",
    "postfix@-.service",
    "postfix@.service",
    "dovecot.service",
    "urmaild.service",
    "urmaild-agent.service",
)
SYSTEMCTL = "/usr/bin/systemctl"


def unit_active_state(unit, systemctl=SYSTEMCTL):
    """Return the systemd ActiveState of a unit via a fixed argv path."""
    out = subprocess.check_output(
        [systemctl, "show", unit, "--property=ActiveState", "--value"],
        env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
    return out.decode().strip()


def canonical_bytes(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":")).encode()


def attestation_digest(binding, extra=None):
    payload = {"binding": binding}
    if extra:
        payload.update(extra)
    return digest(canonical_bytes(payload))


def validate_attestation(attestation, expected_digest, label):
    if not isinstance(attestation, dict):
        raise ValueError(label + " attestation missing")
    entries = attestation.get("units")
    if not isinstance(entries, list) or not entries:
        raise ValueError(label + " attestation has no units")
    seen = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError(label + " attestation entry malformed")
        unit, ts, state = entry.get("unit"), entry.get("ts"), entry.get("state")
        if not isinstance(unit, str) or unit not in QUIESCENCE_UNITS or unit in seen:
            raise ValueError(label + " attestation unit invalid or duplicated: " + str(unit))
        if not isinstance(ts, str) or not ts:
            raise ValueError(label + " attestation missing timestamp for " + unit)
        if state != "inactive":
            raise ValueError(label + " attestation unit not inactive: " + unit)
        seen[unit] = True
    for unit in QUIESCENCE_UNITS:
        if unit not in seen:
            raise ValueError(label + " attestation missing unit: " + unit)
    if attestation.get("binding_sha256") != expected_digest:
        raise ValueError(label + " attestation is not bound to this checkpoint")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(path):
    if not isinstance(path, str) or not path.startswith("/") or str(PurePosixPath(path)) != path or ".." in PurePosixPath(path).parts:
        raise ValueError("noncanonical absolute path")
    return path


class Checkpoint:
    # root/owner are internal dependency injection for tests, never CLI/env flags.
    def __init__(self, root=Path("/"), owner=0):
        self.root = Path(root)
        self.owner = owner

    def path(self, name):
        return self.root / canonical(name).lstrip("/")

    def parents(self, path, include=False, missing_ok=False):
        relative = path.relative_to(self.root)
        current = self.root
        parts = relative.parts if include else relative.parts[:-1]
        for part in parts:
            current = current / part
            try:
                info = current.lstat()
            except FileNotFoundError:
                if missing_ok:
                    return
                raise
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != self.owner or info.st_mode & 0o022:
                raise ValueError("unsafe directory: " + str(current))

    def backup(self, name):
        canonical(name)
        if not name.startswith(BASE + "/") or "/" in name[len(BASE) + 1:]:
            raise ValueError("backup directory outside allowlist")
        path = self.path(name)
        self.parents(path, include=True)
        if stat.S_IMODE(path.lstat().st_mode) != 0o700:
            raise ValueError("backup directory must be 0700")
        return path

    def read(self, path, private=False):
        self.parents(path)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != self.owner or info.st_mode & 0o022:
                raise ValueError("unsafe regular file: " + str(path))
            if private and info.st_mode & 0o077:
                raise ValueError("checkpoint artifact must be private: " + str(path))
            return stream.read()

    def binding(self):
        result = {}
        values = {}
        for name in ("upgrade.hold", "upgrade_receipt.json"):
            raw = self.read(self.path(MAINT + "/" + name))
            values[name] = json.loads(raw)
            result[name] = digest(raw)
        hold, receipt = values["upgrade.hold"], values["upgrade_receipt.json"]
        if (receipt.get("upgrade_held") is not True
                or not hold.get("created_at") or hold["created_at"] != receipt.get("created_at")):
            raise ValueError("invalid or mismatched upgrade fence receipt")
        if hold.get("version") == 2 and receipt.get("version") == 2:
            # v2 binds the exact hold bytes into the receipt and carries the
            # schema generation; the receipt units must mirror the hold units.
            if receipt.get("hold_sha256") != result["upgrade.hold"]:
                raise ValueError("v2 receipt hold digest mismatch")
            generation = receipt.get("generation")
            if not isinstance(generation, int) or isinstance(generation, bool) or generation <= 0:
                raise ValueError("v2 receipt requires a positive generation")
            hold_units = hold.get("original_active_units")
            receipt_units = receipt.get("original_active_units")
            if receipt_units is not None and not (isinstance(receipt_units, list)
                    and all(isinstance(u, str) for u in receipt_units)):
                raise ValueError("v2 receipt units malformed")
            if hold_units is not None and hold_units != receipt_units:
                raise ValueError("v2 hold and receipt units mismatch")
        elif hold.get("version") == 1 and receipt.get("version") == 1:
            # Legacy v1 fences keep the created_at binding only; older live
            # fences remain checkpointable.
            pass
        else:
            raise ValueError("invalid or mismatched upgrade fence receipt")
        return result

    def quiescence(self, query):
        """Sample every writer/template unit; each must be inactive now."""
        entries = []
        for unit in QUIESCENCE_UNITS:
            state = query(unit)
            if state != "inactive":
                raise ValueError("writer unit not inactive: " + unit + "=" + str(state))
            entries.append({"unit": unit, "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "state": state})
        return entries

    def put(self, directory, name, data):
        path = directory / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())

    def syncdir(self, directory):
        fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def record(self, path):
        self.parents(path)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                    or info.st_uid != self.owner or stat.S_IMODE(info.st_mode) != 0o600):
                raise ValueError("unsafe checkpoint artifact: " + str(path))
            sha = hashlib.sha256()
            size = 0
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                size += len(chunk)
                sha.update(chunk)
            return {"sha256": sha.hexdigest(), "size": size, "mode": stat.S_IMODE(info.st_mode)}

    def archive(self, directory):
        inventory = []
        absent = []
        fd = os.open(directory / "state.tar", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as stream, tarfile.open(fileobj=stream, mode="w", dereference=False) as archive:
            def add(name):
                path = self.path(name)
                self.parents(path)
                info = path.lstat()
                entry = {"path": name, "mode": stat.S_IMODE(info.st_mode),
                         "uid": info.st_uid, "gid": info.st_gid}
                if stat.S_ISLNK(info.st_mode):
                    target = os.readlink(path)
                    member = archive.gettarinfo(str(path), arcname=name.lstrip("/"))
                    archive.addfile(member)
                    entry.update(type="symlink", size=len(os.fsencode(target)), sha256=digest(os.fsencode(target)), link=target)
                elif stat.S_ISDIR(info.st_mode):
                    self.parents(path, include=True)
                    archive.addfile(archive.gettarinfo(str(path), arcname=name.lstrip("/")))
                    entry.update(type="directory", size=0, sha256=digest(b""))
                elif stat.S_ISREG(info.st_mode):
                    # gettarinfo may encode hard links; reject hardlinks instead.
                    raw = self.read(path)
                    member = archive.gettarinfo(str(path), arcname=name.lstrip("/"))
                    import io
                    archive.addfile(member, io.BytesIO(raw))
                    entry.update(type="file", size=len(raw), sha256=digest(raw))
                else:
                    raise ValueError("unsupported state node: " + name)
                inventory.append(entry)
                if entry["type"] == "directory":
                    for child in sorted(path.iterdir()):
                        add(name + "/" + child.name)
            for name in ROOTS:
                self.parents(self.path(name), missing_ok=True)
                if not os.path.lexists(self.path(name)):
                    absent.append(name)
                    continue
                add(name)
        # tarfile closes after writing its trailer; persist the complete archive.
        fd = os.open(directory / "state.tar", os.O_RDONLY | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        return inventory, absent

    def create(self, name, package_query=None, unit_state_query=None):
        directory = self.backup(name)
        if set(p.name for p in directory.iterdir()) != {"database.dump", "urmaild.env", "agent.env"}:
            raise ValueError("create requires only database.dump, urmaild.env, agent.env; partial checkpoints cannot be reused")
        if unit_state_query is None:
            unit_state_query = unit_active_state
        binding = self.binding()
        # Quiescence sampling #1: before any artifact is read or written.
        attestation = {"units": self.quiescence(unit_state_query),
                       "binding_sha256": attestation_digest(binding)}
        for artifact in ("database.dump", "urmaild.env", "agent.env"):
            if not self.record(directory / artifact)["size"]:
                raise ValueError("empty input: " + artifact)
            fd = os.open(directory / artifact, os.O_RDONLY | os.O_NOFOLLOW)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        live_env = self.read(self.path("/etc/urmail/urmaild.env"))
        if live_env != self.read(directory / "urmaild.env"):
            raise ValueError("daemon environment changed since dump")
        if self.read(self.path("/etc/urmail/agent.env")) != self.read(directory / "agent.env"):
            raise ValueError("agent environment changed since dump")
        key = "/etc/urmail/master.key"
        for line in live_env.decode().splitlines():
            field, sep, value = line.strip().partition("=")
            if sep and field == "URMAIL_MASTER_KEY_PATH":
                key = value.strip()
                if key[:1] in ("'", '"') and key[-1:] == key[:1]:
                    key = key[1:-1]
        canonical(key)
        if any(key == p or key.startswith(p + "/") for p in ("/dev", "/proc", "/sys", "/var/mail")):
            raise ValueError("master key path outside regular configuration/state")
        raw = self.read(self.path(key))
        if not raw or self.path(key).lstat().st_mode & 0o007:
            raise ValueError("master key must be nonempty and not world-accessible")
        self.put(directory, "master.key", raw)
        agent_key = key
        for line in self.read(directory / "agent.env", private=True).decode().splitlines():
            field, sep, value = line.strip().partition("=")
            if sep and field == "URMAIL_MASTER_KEY_PATH" and value.strip():
                agent_key = value.strip()
                if agent_key[:1] in ("\"", "'") and agent_key[-1:] == agent_key[:1]:
                    agent_key = agent_key[1:-1]
        canonical(agent_key)
        if any(agent_key == p or agent_key.startswith(p + "/") for p in ("/dev", "/proc", "/sys", "/var/mail")):
            raise ValueError("agent master key path outside regular configuration/state")
        agent_raw = self.read(self.path(agent_key))
        if not agent_raw or self.path(agent_key).lstat().st_mode & 0o007:
            raise ValueError("agent master key must be nonempty and not world-accessible")
        self.put(directory, "agent-master.key", agent_raw)
        for binary in BINARIES:
            if not self.read(self.path(binary)):
                raise ValueError("missing/empty installed binary")
        for artifact in ("upgrade.hold", "upgrade_receipt.json"):
            self.put(directory, artifact, self.read(self.path(MAINT + "/" + artifact)))
        if package_query is None:
            package_query = lambda: subprocess.check_output(
                ["/usr/bin/dpkg-query", "-W", "-f=${binary:Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n"],
                env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
        packages = package_query().decode()
        if not any(line.startswith("urmail\t") and line.endswith("\tinstalled") for line in packages.splitlines()):
            raise ValueError("dpkg metadata must include installed urmail package")
        self.put(directory, "packages.json", json.dumps({"dpkg_query": packages, "rollback_scope": "executable bytes only; not complete package/OS rollback"}, sort_keys=True).encode())
        inventory, absent = self.archive(directory)
        if binding != self.binding():
            raise ValueError("fence changed during checkpoint")
        # Quiescence sampling #2: after full capture, bound to the fence binding
        # and the digests of the database dump and the completed state archive.
        dump_sha = self.record(directory / "database.dump")["sha256"]
        state_sha = self.record(directory / "state.tar")["sha256"]
        final_binding = attestation_digest(binding, {"database_dump": dump_sha, "state_tar": state_sha})
        attestation_final = {"units": self.quiescence(unit_state_query),
                             "binding_sha256": final_binding}
        key_info = self.path(key).lstat()
        agent_key_info = self.path(agent_key).lstat()
        manifest = {"version": 2, "binding": binding, "master_key_source": key, "agent_master_key_source": agent_key,
                    "master_key_metadata": {"mode": stat.S_IMODE(key_info.st_mode), "uid": key_info.st_uid, "gid": key_info.st_gid},
                    "agent_master_key_metadata": {"mode": stat.S_IMODE(agent_key_info.st_mode), "uid": agent_key_info.st_uid, "gid": agent_key_info.st_gid},
                    "roots": list(ROOTS), "absent_roots": absent, "inventory": inventory,
                    "artifacts": {a: self.record(directory / a) for a in sorted(ARTIFACTS)},
                    "quiescence_attestation": attestation,
                    "quiescence_attestation_final": attestation_final,
                    "rollback_scope": "Not complete package/OS rollback; excludes PostgreSQL physical data and mail/archive payloads"}
        data = json.dumps(manifest, sort_keys=True, indent=2).encode() + b"\n"
        self.put(directory, "manifest.json", data)
        self.syncdir(directory)
        self.put(directory, ".complete.tmp", (digest(data) + "\n").encode())
        os.rename(directory / ".complete.tmp", directory / "COMPLETE")
        self.syncdir(directory)
        self.verify(name)

    def verify(self, name):
        directory = self.backup(name)
        if set(p.name for p in directory.iterdir()) != ARTIFACTS | {"manifest.json", "COMPLETE"}:
            raise ValueError("partial checkpoint or unexpected artifacts")
        data = self.read(directory / "manifest.json", private=True)
        if self.read(directory / "COMPLETE", private=True) != (digest(data) + "\n").encode():
            raise ValueError("invalid complete marker/manifest hash")
        manifest = json.loads(data)
        if manifest.get("version") != 2 or manifest.get("roots") != list(ROOTS) or set(manifest.get("artifacts", {})) != ARTIFACTS:
            # v2 manifests are required: legacy v1 manifests carry no quiescence
            # attestation and fail closed here (re-run checkpoint create).
            raise ValueError("invalid checkpoint schema/allowlist (v1 manifests are rejected; recreate checkpoint)")
        if manifest.get("binding") != self.binding():
            raise ValueError("checkpoint belongs to another upgrade fence/receipt")
        binding = manifest["binding"]
        dump_sha = manifest["artifacts"]["database.dump"]["sha256"]
        state_sha = manifest["artifacts"]["state.tar"]["sha256"]
        validate_attestation(manifest.get("quiescence_attestation"), attestation_digest(binding), "pre-capture")
        validate_attestation(manifest.get("quiescence_attestation_final"),
                             attestation_digest(binding, {"database_dump": dump_sha, "state_tar": state_sha}),
                             "post-capture")
        for artifact, expected in manifest["artifacts"].items():
            if self.record(directory / artifact) != expected:
                raise ValueError("artifact size/mode/hash mismatch: " + artifact)
        if manifest["artifacts"]["database.dump"]["size"] == 0:
            raise ValueError("empty database dump")
        for artifact, expected in manifest["binding"].items():
            if digest(self.read(directory / artifact, private=True)) != expected:
                raise ValueError("saved receipt does not match fence")
        return manifest


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=("create", "verify"))
    parser.add_argument("path")
    args = parser.parse_args(argv)
    if os.geteuid() != 0:
        parser.exit(1, "checkpoint requires root\n")
    os.umask(0o077)
    try:
        getattr(Checkpoint(), args.command)(args.path)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, tarfile.TarError) as exc:
        print("checkpoint " + args.command + " failed (fence retained): " + str(exc), file=sys.stderr)
        return 1
    print("checkpoint " + args.command + " OK (not complete package/OS rollback)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
