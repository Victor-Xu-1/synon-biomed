import importlib.util
import os
from pathlib import Path
from types import SimpleNamespace
import unittest


POLICY_PATH = (
    Path(__file__).resolve().parents[1]
    / "lib"
    / "mcp_servers_common"
    / "tls_policy.py"
)


def load_policy():
    spec = importlib.util.spec_from_file_location("synon_tls_policy_test", POLICY_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TLSContext:
    def __init__(self):
        self.verify_flags = 3
        self.calls = []

    def wrap_socket(self, *args, **kwargs):
        self.calls.append(("socket", args, kwargs, self.verify_flags))
        return self.verify_flags

    def wrap_bio(self, *args, **kwargs):
        self.calls.append(("bio", args, kwargs, self.verify_flags))
        return self.verify_flags


class TLSPolicyTests(unittest.TestCase):
    def setUp(self):
        os.environ.pop("SYNON_MCP_X509_STRICT", None)

    def test_unset_and_invalid_values_keep_strict_default(self):
        policy = load_policy()
        self.assertEqual(policy.apply_posture(), policy.STRICT)
        os.environ["SYNON_MCP_X509_STRICT"] = "invalid"
        self.assertEqual(policy.apply_posture(), policy.STRICT)

    def test_relaxed_posture_clears_only_client_strict_bit_and_is_idempotent(self):
        policy = load_policy()
        fake_ssl = SimpleNamespace(
            VERIFY_X509_STRICT=1,
            SSLContext=TLSContext,
            create_default_context=lambda: TLSContext(),
        )
        policy.ssl = fake_ssl
        os.environ["SYNON_MCP_X509_STRICT"] = "0"

        self.assertEqual(policy.apply_posture(), policy.RELAXED)
        installed_socket = TLSContext.wrap_socket
        installed_bio = TLSContext.wrap_bio
        self.assertEqual(policy.apply_posture(), policy.RELAXED)
        self.assertIs(TLSContext.wrap_socket, installed_socket)
        self.assertIs(TLSContext.wrap_bio, installed_bio)

        client_socket = TLSContext()
        self.assertEqual(client_socket.wrap_socket(object()), 2)
        self.assertEqual(client_socket.verify_flags, 2)

        server_socket = TLSContext()
        self.assertEqual(server_socket.wrap_socket(object(), True), 3)
        self.assertEqual(server_socket.verify_flags, 3)

        client_bio = TLSContext()
        self.assertEqual(client_bio.wrap_bio(object(), object()), 2)
        self.assertEqual(client_bio.verify_flags, 2)

        server_bio = TLSContext()
        self.assertEqual(server_bio.wrap_bio(object(), object(), True), 3)
        self.assertEqual(server_bio.verify_flags, 3)


if __name__ == "__main__":
    unittest.main()
