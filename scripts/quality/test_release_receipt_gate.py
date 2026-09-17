from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import hmac
import json
import os
from pathlib import Path
import tempfile
import unittest
import uuid

from scripts.quality import release_candidate_manifest as candidate
from scripts.quality import release_receipt_gate as receipt
from scripts.quality.test_release_candidate_manifest import CandidateFixture


NOW = datetime(2026, 9, 11, 0, 30, tzinfo=timezone.utc)


class ReceiptFixture:
    def __init__(self, root: Path) -> None:
        self.candidate = CandidateFixture(root)
        self.manifest = candidate.create(
            self.candidate.repo,
            self.candidate.artifacts,
            123,
            2,
        )
        self.key = root / "authorization.key"
        self.key.write_bytes(b"k" * 32)
        self.key.chmod(0o600)
        self.receipt = root / "authorization.json"
        self.payload = {
            "receipt_id": str(uuid.UUID("12345678-1234-4234-8234-123456789abc")),
            "decision": "authorized",
            "authorized_by": "user",
            "authorization_reference": "controller-task:user-message-430462267",
            "repository": self.manifest["repository"],
            "candidate_run_id": self.manifest["candidate_run_id"],
            "candidate_run_attempt": self.manifest["candidate_run_attempt"],
            "source_commit": self.manifest["source_commit"],
            "source_tree": self.manifest["source_tree"],
            "product_version": self.manifest["product_identity"]["version"],
            "tag": f"v{self.manifest['product_identity']['version']}",
            "candidate_manifest_sha256": candidate.digest(
                self.candidate.artifacts / candidate.MANIFEST_NAME
            ),
            "artifact_set_sha256": self.manifest["artifact_set_sha256"],
            "issued_at": "2026-09-11T00:00:00Z",
            "expires_at": "2026-09-11T01:00:00Z",
        }
        self.write()

    def write(self, *, key: bytes | None = None) -> None:
        signature = hmac.new(
            key if key is not None else self.key.read_bytes(),
            receipt.canonical_payload(self.payload),
            hashlib.sha256,
        ).hexdigest()
        self.receipt.write_text(
            json.dumps(
                {
                    "schema": receipt.SCHEMA,
                    "payload": self.payload,
                    "signature": {
                        "algorithm": "hmac-sha256",
                        "value": signature,
                    },
                },
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )


class ReleaseReceiptGateTests(unittest.TestCase):
    def test_valid_receipt_authorizes_only_the_exact_candidate(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            result = receipt.verify(
                fixture.candidate.repo,
                fixture.candidate.artifacts,
                fixture.receipt,
                fixture.key,
                NOW,
            )
            self.assertTrue(result["authorized"])
            self.assertEqual(result["source_commit"], fixture.manifest["source_commit"])
            self.assertEqual(result["artifact_set_sha256"], fixture.manifest["artifact_set_sha256"])
            self.assertNotIn("signature", result)

    def test_wrong_signature_and_candidate_binding_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            fixture.write(key=b"x" * 32)
            with self.assertRaisesRegex(receipt.ReceiptError, "signature_invalid"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    fixture.receipt,
                    fixture.key,
                    NOW,
                )

        for field, value in (
            ("source_commit", "f" * 40),
            ("source_tree", "e" * 40),
            ("product_version", "1.2.3"),
            ("candidate_manifest_sha256", "d" * 64),
            ("artifact_set_sha256", "c" * 64),
        ):
            with self.subTest(field=field), tempfile.TemporaryDirectory(
                prefix="synon-release-receipt-"
            ) as directory:
                fixture = ReceiptFixture(Path(directory))
                fixture.payload[field] = value
                if field == "product_version":
                    fixture.payload["tag"] = "v1.2.3"
                fixture.write()
                with self.assertRaisesRegex(receipt.ReceiptError, "binding_mismatch"):
                    receipt.verify(
                        fixture.candidate.repo,
                        fixture.candidate.artifacts,
                        fixture.receipt,
                        fixture.key,
                        NOW,
                    )

    def test_expired_future_and_excessive_ttl_are_rejected(self) -> None:
        cases = [
            ("2026-09-10T00:00:00Z", "2026-09-10T01:00:00Z"),
            ("2026-09-12T00:00:00Z", "2026-09-12T01:00:00Z"),
            ("2026-09-10T00:00:00Z", "2026-09-12T00:00:01Z"),
        ]
        for issued, expires in cases:
            with self.subTest(issued=issued), tempfile.TemporaryDirectory(
                prefix="synon-release-receipt-"
            ) as directory:
                fixture = ReceiptFixture(Path(directory))
                fixture.payload["issued_at"] = issued
                fixture.payload["expires_at"] = expires
                fixture.write()
                with self.assertRaisesRegex(receipt.ReceiptError, "expired_or_not_yet_valid"):
                    receipt.verify(
                        fixture.candidate.repo,
                        fixture.candidate.artifacts,
                        fixture.receipt,
                        fixture.key,
                        NOW,
                    )

    def test_naive_verification_clock_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            with self.assertRaisesRegex(receipt.ReceiptError, "receipt_now_invalid"):
                receipt.validate_payload(
                    fixture.payload,
                    86400,
                    datetime(2026, 9, 11, 0, 30),
                )

    def test_decision_authority_tag_and_id_are_strict(self) -> None:
        cases = [
            ("decision", "pending", "payload_value_invalid"),
            ("authorized_by", "controller", "payload_value_invalid"),
            ("authorization_reference", "", "payload_value_invalid"),
            ("receipt_id", "not-a-uuid", "receipt_id_invalid"),
            ("tag", "0.1.0", "payload_value_invalid"),
        ]
        for field, value, error in cases:
            with self.subTest(field=field), tempfile.TemporaryDirectory(
                prefix="synon-release-receipt-"
            ) as directory:
                fixture = ReceiptFixture(Path(directory))
                fixture.payload[field] = value
                fixture.write()
                with self.assertRaisesRegex(receipt.ReceiptError, error):
                    receipt.verify(
                        fixture.candidate.repo,
                        fixture.candidate.artifacts,
                        fixture.receipt,
                        fixture.key,
                        NOW,
                    )

    def test_key_permissions_location_and_link_count_are_strict(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            fixture.key.chmod(0o644)
            with self.assertRaisesRegex(receipt.ReceiptError, "receipt_key_invalid"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    fixture.receipt,
                    fixture.key,
                    NOW,
                )

        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            inside = fixture.candidate.repo / "authorization.key"
            inside.write_bytes(b"k" * 32)
            inside.chmod(0o600)
            with self.assertRaisesRegex(receipt.ReceiptError, "key_inside_source"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    fixture.receipt,
                    inside,
                    NOW,
                )

        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            os.link(fixture.key, Path(directory) / "key-copy")
            with self.assertRaisesRegex(receipt.ReceiptError, "receipt_key_invalid"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    fixture.receipt,
                    fixture.key,
                    NOW,
                )

    def test_receipt_must_be_external_regular_unique_json(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            inside = fixture.candidate.repo / "authorization.json"
            inside.write_bytes(fixture.receipt.read_bytes())
            with self.assertRaisesRegex(receipt.ReceiptError, "receipt_inside_source"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    inside,
                    fixture.key,
                    NOW,
                )

        with tempfile.TemporaryDirectory(prefix="synon-release-receipt-") as directory:
            fixture = ReceiptFixture(Path(directory))
            fixture.receipt.write_text(
                '{"schema":"one","schema":"two"}\n',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(receipt.ReceiptError, "json_duplicate_key"):
                receipt.verify(
                    fixture.candidate.repo,
                    fixture.candidate.artifacts,
                    fixture.receipt,
                    fixture.key,
                    NOW,
                )


if __name__ == "__main__":
    unittest.main()
