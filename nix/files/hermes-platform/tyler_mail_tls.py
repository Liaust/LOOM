"""Keep Bridge's CA as a trust anchor, never as its server end-entity."""
from datetime import datetime, timedelta, timezone
import hashlib
import imaplib
import ipaddress
import json
import os
from pathlib import Path
import ssl
import subprocess
import time
import uuid

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID


class CertificateRoleError(ValueError):
    pass


def peer_certificate(trust=None):
    client = imaplib.IMAP4("127.0.0.1", 1143, timeout=15)
    try:
        context = ssl.create_default_context(cafile=str(trust)) if trust else ssl._create_unverified_context()
        client.starttls(context)
        return client.sock.getpeercert(binary_form=True)
    finally:
        try:
            client.logout()
        except (OSError, imaplib.IMAP4.error):
            pass


def private_write(path, raw):
    with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def trust_file(binding):
    path = Path(binding["private"]) / "bridge-cert.pem"
    if path.is_symlink():
        raise ValueError("Bridge trust anchor must be a private regular file")
    if not path.exists():
        # No credentials are sent during first-use trust of this fixed local service.
        der = peer_certificate()
        private_write(path, ssl.DER_cert_to_PEM_cert(der).encode())
    return path


def certificate(binding):
    path = trust_file(binding)
    actual = x509.load_der_x509_certificate(peer_certificate(path))
    if actual.extensions.get_extension_for_class(x509.BasicConstraints).value.ca:
        raise CertificateRoleError("Bridge uses its CA as server certificate; operator TLS repair required")
    # The validated chain and hostname bind the leaf to this pinned CA. A leaf
    # is not expected to have the same DER bytes as its issuer/trust anchor.
    return path


def server_material(ca, ca_key):
    public = lambda key: key.public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)
    if public(ca_key.public_key()) != public(ca.public_key()):
        raise ValueError("exported CA key does not match the pinned issuer")
    if not ca.extensions.get_extension_for_class(x509.BasicConstraints).value.ca:
        raise ValueError("issuer is not a CA")
    if not ca.extensions.get_extension_for_class(x509.KeyUsage).value.key_cert_sign:
        raise ValueError("issuer may not sign certificates")
    now = datetime.now(timezone.utc)
    expires = min(now + timedelta(days=365), ca.not_valid_after_utc - timedelta(minutes=1))
    if expires <= now + timedelta(days=1):
        raise ValueError("local CA needs operator renewal")
    key = ec.generate_private_key(ec.SECP256R1())
    try:
        issuer_id = ca.extensions.get_extension_for_class(x509.SubjectKeyIdentifier).value
    except x509.ExtensionNotFound:
        issuer_id = x509.SubjectKeyIdentifier.from_public_key(ca.public_key())
    leaf = (x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "localhost")]))
        .issuer_name(ca.subject).public_key(key.public_key()).serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=5)).not_valid_after(expires)
        .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
        .add_extension(x509.KeyUsage(digital_signature=True, content_commitment=False, key_encipherment=False,
            data_encipherment=False, key_agreement=False, key_cert_sign=False, crl_sign=False,
            encipher_only=False, decipher_only=False), critical=True)
        .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False)
        .add_extension(x509.SubjectAlternativeName([x509.DNSName("localhost"),
            x509.IPAddress(ipaddress.ip_address("127.0.0.1")), x509.IPAddress(ipaddress.ip_address("::1"))]), critical=False)
        .add_extension(x509.SubjectKeyIdentifier.from_public_key(key.public_key()), critical=False)
        .add_extension(x509.AuthorityKeyIdentifier.from_issuer_subject_key_identifier(issuer_id), critical=False)
        .sign(ca_key, hashes.SHA256()))
    return leaf, key


def bridge_cli(binding, env, lines):
    subprocess.run([binding["bridge"], "--cli"], input="\n".join(lines) + "\n", env=env,
                   cwd=binding["home"], capture_output=True, text=True, timeout=60, check=True)


def prepare_certificate(binding, control, env):
    try:
        return certificate(binding)
    except (CertificateRoleError, ssl.SSLCertVerificationError):
        pass
    trust = trust_file(binding)
    ca_bytes = trust.read_bytes()
    ca = x509.load_pem_x509_certificate(ca_bytes)
    before = peer_certificate()
    observed = x509.load_der_x509_certificate(before)
    if ca.public_bytes(serialization.Encoding.DER) != before:
        # A previously issued leaf may need repair, but a changed issuer is
        # never automatically trusted. This probe sends no credentials.
        observed.verify_directly_issued_by(ca)
        if observed.extensions.get_extension_for_class(x509.BasicConstraints).value.ca:
            raise ValueError("unexpected server CA differs from the pinned certificate; preserve and review")
    stage = Path(binding["cache"]) / ("tls-repair-" + uuid.uuid4().hex)
    stage.mkdir(mode=0o700)
    control(binding, "is-active", capture_output=True)
    control(binding, "stop")
    try:
        bridge_cli(binding, env, ["cert export", str(stage), "exit"])
        exported = x509.load_pem_x509_certificate((stage / "cert.pem").read_bytes())
        if exported.public_bytes(serialization.Encoding.DER) != before:
            raise ValueError("exported certificate changed during repair")
        # This issuer credential remains encrypted in native selected state;
        # diagnostic/export staging is outside the captured profile.
        entry = "loom-tyler-proton-tls-ca/" + ca.fingerprint(hashes.SHA256()).hex()
        encrypted = Path(env["PASSWORD_STORE_DIR"]) / (entry + ".gpg")
        if exported.fingerprint(hashes.SHA256()) == ca.fingerprint(hashes.SHA256()):
            key_bytes = (stage / "key.pem").read_bytes()
        elif encrypted.is_file():
            key_bytes = subprocess.run([binding["pass"], "show", entry], env=env,
                                       capture_output=True, timeout=30, check=True).stdout
        else:
            raise ValueError("pinned issuer key unavailable; preserve existing state and request review")
        ca_key = serialization.load_pem_private_key(key_bytes, password=None)
        leaf, key = server_material(ca, ca_key)
        if not encrypted.exists():
            subprocess.run([binding["pass"], "insert", "--multiline", entry], input=key_bytes,
                           env=env, capture_output=True, timeout=30, check=True)
        # Bridge imports paths, not bytes: its live pair must survive cache
        # eviction and be selected with the private native profile capture.
        active = Path(binding["private"]) / stage.name
        active.mkdir(mode=0o700)
        private_write(active / "server-cert.pem", leaf.public_bytes(serialization.Encoding.PEM) + ca_bytes)
        private_write(active / "server-key.pem", key.private_bytes(serialization.Encoding.PEM,
            serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
        bridge_cli(binding, env, ["cert import", str(active / "server-cert.pem"), str(active / "server-key.pem"), "exit"])
    finally:
        control(binding, "start")
    for attempt in range(30):
        try:
            result = certificate(binding)
            break
        except (OSError, imaplib.IMAP4.abort):
            if attempt == 29:
                raise
            time.sleep(0.5)
    receipt = dict(trust_anchor_sha256=hashlib.sha256(trust.read_bytes()).hexdigest(),
                   server_certificate_sha256=leaf.fingerprint(hashes.SHA256()).hex(),
                   credentials_reset=False, mailbox_mutations=False)
    private_write(stage / "receipt.json", (json.dumps(receipt, sort_keys=True) + "\n").encode())
    return result
