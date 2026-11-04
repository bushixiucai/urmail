#!/usr/bin/env python3
"""Quiescence attestation and v2 fence binding tests (unprivileged sandbox)."""
import json
from pathlib import Path
import unittest

import test_upgrade_checkpoint as fixtures

cp = fixtures.cp


class QuiescenceAndV2BindingTests(fixtures.CheckpointTests):
    def v2fence(self):
        hold = {"version": 2, "created_at": "2026-11-03T00:00:00Z",
                "generation": 155, "original_active_units": ["postfix.service"]}
        hold_raw = json.dumps(hold).encode()
        receipt = {"version": 2, "created_at": "2026-11-03T00:00:00Z",
                   "hold_sha256": cp.digest(hold_raw), "generation": 155,
                   "original_active_units": ["postfix.service"], "upgrade_held": True}
        self.put(cp.MAINT + "/upgrade.hold", hold_raw)
        self.put(cp.MAINT + "/upgrade_receipt.json", json.dumps(receipt).encode())

    def create_v2(self):
        self.v2fence()
        self.create()

    def rewrite_manifest(self, mutate):
        data = (self.directory / "manifest.json").read_bytes()
        manifest = json.loads(data)
        mutate(manifest)
        data = json.dumps(manifest, sort_keys=True, indent=2).encode() + b"\n"
        (self.directory / "manifest.json").write_bytes(data)
        (self.directory / "COMPLETE").write_bytes((cp.digest(data) + "\n").encode())

    def test_v2_create_verify_passes_and_requires_attestation(self):
        self.create_v2()
        manifest = self.helper.verify(self.name)
        self.assertEqual(manifest["version"], 2)
        for field in ("quiescence_attestation", "quiescence_attestation_final"):
            units = [entry["unit"] for entry in manifest[field]["units"]]
            self.assertEqual(sorted(units), sorted(cp.QUIESCENCE_UNITS))
            self.assertTrue(all(entry["state"] == "inactive" for entry in manifest[field]["units"]))

    def test_active_unit_pre_capture_rejected(self):
        self.v2fence()
        with self.assertRaisesRegex(ValueError, "writer unit not inactive: nginx.service"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n",
                               lambda unit: "active" if unit == "nginx.service" else "inactive")
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_active_unit_post_capture_rejected(self):
        self.v2fence()
        calls = {"n": 0}

        def query(unit):
            calls["n"] += 1
            return "active" if calls["n"] > len(cp.QUIESCENCE_UNITS) else "inactive"

        with self.assertRaisesRegex(ValueError, "writer unit not inactive"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n", query)
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_query_failure_fails_closed(self):
        self.v2fence()

        def query(_unit):
            raise OSError("systemctl unavailable")

        with self.assertRaises(OSError):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n", query)
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_missing_attestation_rejected(self):
        self.create_v2()
        self.rewrite_manifest(lambda m: m.pop("quiescence_attestation_final"))
        with self.assertRaisesRegex(ValueError, "post-capture attestation missing"):
            self.helper.verify(self.name)

    def test_attestation_unit_tamper_rejected(self):
        self.create_v2()
        self.rewrite_manifest(lambda m: m["quiescence_attestation"]["units"][0].update(state="active"))
        with self.assertRaisesRegex(ValueError, "pre-capture attestation unit not inactive"):
            self.helper.verify(self.name)

    def test_attestation_digest_binding_rejected(self):
        self.create_v2()
        self.rewrite_manifest(lambda m: m["quiescence_attestation"].update(binding_sha256="0" * 64))
        with self.assertRaisesRegex(ValueError, "pre-capture attestation is not bound"):
            self.helper.verify(self.name)

    def test_v1_manifest_rejected_fail_closed(self):
        self.create_v2()
        self.rewrite_manifest(lambda m: m.update(version=1))
        with self.assertRaisesRegex(ValueError, "v1 manifests are rejected"):
            self.helper.verify(self.name)

    def test_v2_receipt_hold_digest_mismatch_rejected(self):
        self.v2fence()
        receipt_path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        receipt = json.loads(receipt_path.read_bytes())
        receipt["hold_sha256"] = "f" * 64
        receipt_path.write_bytes(json.dumps(receipt).encode())
        with self.assertRaisesRegex(ValueError, "v2 receipt hold digest mismatch"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n",
                               lambda unit: "inactive")

    def test_v2_receipt_generation_required(self):
        self.v2fence()
        receipt_path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        receipt = json.loads(receipt_path.read_bytes())
        del receipt["generation"]
        receipt_path.write_bytes(json.dumps(receipt).encode())
        with self.assertRaisesRegex(ValueError, "positive generation"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n",
                               lambda unit: "inactive")

    def test_v2_hold_receipt_units_mismatch_rejected(self):
        self.v2fence()
        hold_path = self.helper.path(cp.MAINT + "/upgrade.hold")
        hold = json.loads(hold_path.read_bytes())
        hold["original_active_units"] = ["dovecot.service"]
        hold_raw = json.dumps(hold).encode()
        hold_path.write_bytes(hold_raw)
        receipt_path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        receipt = json.loads(receipt_path.read_bytes())
        receipt["hold_sha256"] = cp.digest(hold_raw)
        receipt_path.write_bytes(json.dumps(receipt).encode())
        with self.assertRaisesRegex(ValueError, "units mismatch"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n",
                               lambda unit: "inactive")

    def test_legacy_v1_fence_still_verifiable(self):
        # v1 live fences keep the created_at-only binding and remain checkpointable.
        self.create()
        manifest = self.helper.verify(self.name)
        self.assertEqual(manifest["version"], 2)
        self.assertIn("quiescence_attestation", manifest)

    def test_mixed_versions_rejected(self):
        self.v2fence()
        receipt_path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        receipt = json.loads(receipt_path.read_bytes())
        receipt["version"] = 1
        receipt.pop("hold_sha256", None)
        receipt_path.write_bytes(json.dumps(receipt).encode())
        with self.assertRaisesRegex(ValueError, "invalid or mismatched upgrade fence receipt"):
            self.helper.create(self.name, lambda: b"urmail\t1\tarm64\tinstalled\n",
                               lambda unit: "inactive")


if __name__ == "__main__":
    unittest.main()
