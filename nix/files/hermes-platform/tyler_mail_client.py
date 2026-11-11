"""Use standard clients with the fixed local Bridge and private trust anchor."""
import json
import os
from pathlib import Path
import re
import shlex
import smtplib
import ssl
import stat
import subprocess
import uuid

import tomlkit

from tyler_mail_tls import private_write


def sender_identity(binding):
    address = binding.get("priority_address")
    name = binding.get("display_name")
    if (not isinstance(address, str) or len(address) > 320
            or not re.fullmatch(r'[^@\s"<>]+@[^@\s"<>]+\.[^@\s"<>]+', address)
            or not isinstance(name, str) or not name.strip() or len(name) > 256
            or any(ord(char) < 32 for char in name)):
        raise ValueError("explicit operator-verified mail sender identity required")
    return address, name


def configuration(binding):
    path = Path(binding["environment"]["XDG_CONFIG_HOME"]) / "himalaya/config.toml"
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("mail configuration custody differs; preserve it and request review")
    raw = path.read_bytes()
    return path, raw, tomlkit.parse(raw.decode()).unwrap()


def prepare_sender(binding):
    address, _ = sender_identity(binding)
    path, raw, document = configuration(binding)
    account = document["accounts"]["tyler"]
    if account.get("email") != address:
        raise ValueError("saved sender differs from the selected identity; preserve and review")
    sender = account["message"]["send"]["backend"]
    private = Path(binding["private"])
    command = shlex.join([binding["msmtp"], "--file=" + str(private / "msmtp.conf"), "--account=tyler", "--read-recipients"])
    secret_command = shlex.join([binding["pass"], "show", "loom-tyler-proton-client"])
    if sender.get("type") == "sendmail" and sender.get("cmd") == command:
        return
    expected = dict(type="smtp", host="127.0.0.1", port=1025,
                    encryption=dict(type="start-tls"), login=account["backend"]["login"],
                    auth=dict(type="password", cmd=secret_command))
    if sender != expected:
        raise ValueError("outgoing mail settings differ from the known Bridge setup; preserve and review")
    quoted = json.dumps
    config = ("defaults\nauth on\ntls on\ntls_starttls on\ntls_certcheck on\n"
              "tls_trust_file " + quoted(str(private / "bridge-cert.pem")) + "\n"
              "account tyler\nhost 127.0.0.1\nport 1025\nfrom " + quoted(address) + "\n"
              "user " + quoted(expected["login"]) + "\npasswordeval " + quoted(secret_command) + "\n")
    sender_path = private / "msmtp.conf"
    if sender_path.exists() or sender_path.is_symlink():
        if sender_path.is_symlink() or sender_path.read_text() != config:
            raise ValueError("existing SMTP configuration differs; preserve and review")
    else:
        private_write(sender_path, config.encode())
    private_write(private / ("client-before-" + uuid.uuid4().hex + ".toml"), raw)
    account["message"]["send"]["backend"] = dict(type="sendmail", cmd=command)
    staged = path.parent / (".config-" + uuid.uuid4().hex + ".toml")
    private_write(staged, tomlkit.dumps(document).encode())
    if path.read_bytes() != raw:
        raise ValueError("mail configuration changed concurrently; preserve and review")
    os.replace(staged, path)


def check_clients(binding, env):
    # Native list checks the actual IMAP client and password-command runtime.
    # Himalaya's sendmail doctor would execute a delivery command with no mail;
    # check the sender's safe information mode and SMTP auth/NOOP separately.
    for label, args in (("IMAP folder discovery", [binding["himalaya"], "folder", "list", "--output", "json"]),
                        ("SMTP TLS inspection", [binding["msmtp"], "--file=" + str(Path(binding["private"]) / "msmtp.conf"),
                         "--account=tyler", "--serverinfo"])):
        result = subprocess.run(args, env=env, cwd=binding["home"], capture_output=True, timeout=60)
        if result.returncode:
            raise ValueError(label + " failed; preserve saved credentials and request operator diagnosis")
    _, _, document = configuration(binding)
    login = document["accounts"]["tyler"]["backend"]["login"]
    password = subprocess.run([binding["pass"], "show", "loom-tyler-proton-client"], env=env,
                              capture_output=True, check=True, timeout=30).stdout.decode().splitlines()[0]
    with smtplib.SMTP("127.0.0.1", 1025, timeout=15) as client:
        client.ehlo()
        client.starttls(context=ssl.create_default_context(cafile=env["SSL_CERT_FILE"]))
        client.ehlo()
        client.login(login, password)
        if client.noop()[0] != 250:
            raise ValueError("SMTP authenticated but NOOP check failed; no mail sent")
