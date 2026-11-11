import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import tyler_mail
import tyler_mail_tls as tls
import tyler_mail_client as mail_client
from datetime import datetime, timedelta, timezone
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID


class TylerMailTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.marker = self.root / "owner.json"
        self.marker.write_text(json.dumps(dict(profile="tyler", box_key="company", retired=False)))
        self.marker.chmod(0o600)
        self.binding = dict(profile_marker=str(self.marker), private=str(self.root / "private"),
                            priority_address="selected@example.test", display_name="Selected Sender",
                            environment=dict(HOME="/selected", PATH="/selected/bin"),
                            systemctl="/native/systemctl", unit="loom-tyler-proton-bridge.service")

    def test_real_private_tyler_binding_required(self):
        tyler_mail.check_profile(self.binding)
        for owner in (dict(profile="mina", box_key="company", retired=False),
                      dict(profile="tyler", box_key="personal", retired=False),
                      dict(profile="tyler", box_key="company", retired=True)):
            self.marker.write_text(json.dumps(owner))
            with self.assertRaises(ValueError):
                tyler_mail.check_profile(self.binding)

    def test_shared_or_symlinked_marker_refuses(self):
        self.marker.chmod(0o644)
        with self.assertRaises(ValueError):
            tyler_mail.check_profile(self.binding)
        self.marker.chmod(0o600)
        link = self.root / "link"
        link.symlink_to(self.marker)
        with self.assertRaises(ValueError):
            tyler_mail.check_profile(dict(self.binding, profile_marker=str(link)))

    def test_service_control_does_not_inherit_business_credentials(self):
        with patch.dict(os.environ, BASECAMP_TOKEN="untrusted", GH_TOKEN="untrusted"), \
                patch.object(tyler_mail.subprocess, "run", return_value=SimpleNamespace(stdout="inactive")) as run:
            tyler_mail.control(self.binding, "is-active", check=False, capture_output=True)
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["/native/systemctl", "--user", "is-active", self.binding["unit"]])
        self.assertNotIn("BASECAMP_TOKEN", kwargs["env"])
        self.assertNotIn("GH_TOKEN", kwargs["env"])
        self.assertEqual(kwargs["env"]["XDG_RUNTIME_DIR"], "/run/user/" + str(os.getuid()))

    def test_existing_keychain_is_not_regenerated(self):
        store = Path(self.binding["private"]) / "pass"
        store.mkdir(parents=True, mode=0o700)
        store.parent.chmod(0o700)
        (store / ".gpg-id").write_text("existing-identity\n")
        with patch.object(tyler_mail.subprocess, "run") as run:
            tyler_mail.init_keychain(self.binding)
        run.assert_not_called()
        self.assertEqual((store / ".gpg-id").read_text(), "existing-identity\n")

    def test_keychain_runtime_is_selected_without_importing_ambient_environment(self):
        with patch.dict(os.environ, GH_TOKEN="untrusted", HERMES_HOME="/another-agent"):
            env = tyler_mail.environment(self.binding)
        self.assertEqual(env["XDG_RUNTIME_DIR"], "/run/user/" + str(os.getuid()))
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("HERMES_HOME", env)

    def test_read_defaults_to_preview_without_removing_explicit_write_commands(self):
        self.assertEqual(tyler_mail.client_args(["message", "read", "1"]), ["message", "read", "--preview", "1"])
        self.assertEqual(tyler_mail.client_args(["--output", "json", "message", "read", "1"]),
                         ["--output", "json", "message", "read", "--preview", "1"])
        for args in (["message", "read", "--preview", "1"], ["message", "read", "-p", "1"],
                     ["flag", "add", "1", "seen"], ["folder", "list"]):
            self.assertEqual(tyler_mail.client_args(args), args)

    def test_other_config_selection_is_not_a_personal_account_fallback(self):
        for args in (["--config", "/other", "folder", "list"], ["folder", "list", "--config=/other"],
                     ["-c/other", "message", "read", "1"]):
            with self.assertRaises(ValueError):
                tyler_mail.client_args(args)

    def test_native_client_is_the_setup_gate_without_fix_or_sending(self):
        binding = dict(self.binding, himalaya="/native/himalaya", home="/selected")
        with patch.object(tyler_mail, "certificate", return_value=Path("/pinned-ca")), \
                patch.object(tyler_mail, "check_clients") as check:
            tyler_mail.check_client(binding)
        args, _ = check.call_args
        self.assertEqual(args[0], binding)
        self.assertEqual(args[1]["SSL_CERT_FILE"], "/pinned-ca")
        with patch.object(tyler_mail, "certificate", return_value=Path("/pinned-ca")), \
                patch.object(tyler_mail, "check_clients", side_effect=ValueError("preserve saved credentials")):
            with self.assertRaisesRegex(ValueError, "preserve saved credentials"):
                tyler_mail.check_client(binding)

    def test_sender_migration_preserves_imap_and_existing_config_history(self):
        import shlex
        import tomlkit
        private = self.root / "private"
        private.mkdir(mode=0o700)
        config_home = self.root / "config"
        path = config_home / "himalaya/config.toml"
        path.parent.mkdir(parents=True)
        secret = shlex.join(["/native/pass", "show", "loom-tyler-proton-client"])
        imap = dict(type="imap", login="selected-user", host="127.0.0.1", port=1143,
                    encryption=dict(type="start-tls"), auth=dict(type="password", cmd=secret))
        smtp = dict(type="smtp", login="selected-user", host="127.0.0.1", port=1025,
                    encryption=dict(type="start-tls"), auth=dict(type="password", cmd=secret))
        # The real wizard uses dotted keys, whose nested tomlkit replacement
        # differs from explicit [table] replacement. Preserve semantic values.
        raw = ('[accounts.tyler]\nemail = "selected@example.test"\nbackend.type = "imap"\nbackend.login = "selected-user"\n'
               'backend.host = "127.0.0.1"\nbackend.port = 1143\nbackend.encryption.type = "start-tls"\n'
               'backend.auth.type = "password"\nbackend.auth.cmd = ' + json.dumps(secret) + '\n'
               'message.send.backend.type = "smtp"\nmessage.send.backend.host = "127.0.0.1"\n'
               'message.send.backend.port = 1025\nmessage.send.backend.encryption.type = "start-tls"\n'
               'message.send.backend.login = "selected-user"\nmessage.send.backend.auth.type = "password"\n'
               'message.send.backend.auth.cmd = ' + json.dumps(secret) + '\n').encode()
        path.write_bytes(raw)
        path.chmod(0o600)
        binding = dict(self.binding, msmtp="/native/msmtp", **{"pass": "/native/pass"},
                       environment=dict(XDG_CONFIG_HOME=str(config_home)))
        mail_client.prepare_sender(binding)
        self.assertEqual(tomlkit.parse(path.read_text())["accounts"]["tyler"]["backend"].unwrap(), imap)
        self.assertEqual(next(private.glob("client-before-*.toml")).read_bytes(), raw)
        after = path.read_bytes()
        mail_client.prepare_sender(binding)
        self.assertEqual(path.read_bytes(), after)
        self.assertIn("tls_certcheck on", (private / "msmtp.conf").read_text())
        self.assertNotIn("password ", (private / "msmtp.conf").read_text())
        self.assertIn('from "selected@example.test"', (private / "msmtp.conf").read_text())

    def test_sender_has_no_implicit_operator_identity(self):
        self.assertEqual(mail_client.sender_identity(self.binding), ("selected@example.test", "Selected Sender"))
        for extra in (dict(priority_address=""), dict(priority_address="bad\nfrom another@example.test"),
                      dict(display_name=""), dict(display_name="bad\nname")):
            with self.assertRaises(ValueError):
                mail_client.sender_identity(dict(self.binding, **extra))
        with self.assertRaises(ValueError):
            mail_client.sender_identity({})


class BridgeCertificateTest(unittest.TestCase):
    def setUp(self):
        self.key = ec.generate_private_key(ec.SECP256R1())
        now = datetime.now(timezone.utc)
        name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Bridge test issuer")])
        self.ca = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
            .public_key(self.key.public_key()).serial_number(x509.random_serial_number())
            .not_valid_before(now - timedelta(days=1)).not_valid_after(now + timedelta(days=1000))
            .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
            .add_extension(x509.SubjectKeyIdentifier(b"issuer-supplied-identifier"), critical=False)
            .add_extension(x509.KeyUsage(digital_signature=True, content_commitment=False,
                key_encipherment=False, data_encipherment=False, key_agreement=False,
                key_cert_sign=True, crl_sign=True, encipher_only=False, decipher_only=False), critical=True)
            .sign(self.key, hashes.SHA256()))

    def test_server_leaf_has_distinct_role_hostname_and_existing_issuer(self):
        leaf, key = tls.server_material(self.ca, self.key)
        leaf.verify_directly_issued_by(self.ca)
        self.assertEqual(leaf.issuer, self.ca.subject)
        self.assertEqual(leaf.public_key().public_numbers(), key.public_key().public_numbers())
        self.assertFalse(leaf.extensions.get_extension_for_class(x509.BasicConstraints).value.ca)
        self.assertFalse(leaf.extensions.get_extension_for_class(x509.KeyUsage).value.key_cert_sign)
        self.assertIn(ExtendedKeyUsageOID.SERVER_AUTH,
                      leaf.extensions.get_extension_for_class(x509.ExtendedKeyUsage).value)
        self.assertEqual(leaf.extensions.get_extension_for_class(x509.AuthorityKeyIdentifier).value.key_identifier,
                         self.ca.extensions.get_extension_for_class(x509.SubjectKeyIdentifier).value.digest)
        self.assertIn("localhost", leaf.extensions.get_extension_for_class(x509.SubjectAlternativeName)
                      .value.get_values_for_type(x509.DNSName))
        self.assertLess(leaf.not_valid_after_utc, self.ca.not_valid_after_utc)
        with self.assertRaisesRegex(ValueError, "does not match"):
            tls.server_material(self.ca, ec.generate_private_key(ec.SECP256R1()))

    def test_ca_leaf_is_refused_but_verified_distinct_leaf_is_accepted(self):
        leaf, _ = tls.server_material(self.ca, self.key)
        with patch.object(tls, "trust_file", return_value=Path("/pinned-ca")), \
                patch.object(tls, "peer_certificate", return_value=self.ca.public_bytes(serialization.Encoding.DER)):
            with self.assertRaises(tls.CertificateRoleError):
                tls.certificate({})
        with patch.object(tls, "trust_file", return_value=Path("/pinned-ca")), \
                patch.object(tls, "peer_certificate", return_value=leaf.public_bytes(serialization.Encoding.DER)) as peer:
            self.assertEqual(tls.certificate({}), Path("/pinned-ca"))
            peer.assert_called_once_with(Path("/pinned-ca"))

    def test_correct_existing_certificate_does_not_restart_or_export(self):
        with patch.object(tls, "certificate", return_value=Path("/pinned-ca")), \
                patch.object(tls, "bridge_cli") as native:
            control = unittest.mock.Mock()
            self.assertEqual(tls.prepare_certificate({}, control, {}), Path("/pinned-ca"))
        control.assert_not_called()
        native.assert_not_called()


if __name__ == "__main__":
    unittest.main()
