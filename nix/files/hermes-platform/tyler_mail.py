"""Native Tyler Proton setup and fixed-context Himalaya access; never log secrets."""
import argparse
import getpass
import hashlib
import imaplib
import json
import os
from pathlib import Path
import stat
import shlex
import ssl
import subprocess
import sys

from tyler_mail_tls import certificate, prepare_certificate
from tyler_mail_client import check_clients, prepare_sender, sender_identity


def check_profile(binding):
    marker = Path(binding["profile_marker"])
    info = marker.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError("private Tyler profile unavailable")
    owner = json.loads(marker.read_bytes())
    if owner.get("profile") != "tyler" or owner.get("box_key") != "company" or owner.get("retired") is not False:
        raise ValueError("native profile binding differs")


def environment(binding):
    return dict(binding["environment"], XDG_RUNTIME_DIR="/run/user/" + str(os.getuid()))


def control(binding, *args, check=True, capture_output=False):
    runtime = "/run/user/" + str(os.getuid())
    env = dict(HOME="/home/agents", XDG_RUNTIME_DIR=runtime,
               DBUS_SESSION_BUS_ADDRESS="unix:path=" + runtime + "/bus", LANG="C.UTF-8")
    return subprocess.run([binding["systemctl"], "--user", *args, binding["unit"]],
                          env=env, check=check, capture_output=capture_output, text=True, timeout=30)


def init_keychain(binding):
    private = Path(binding["private"])
    for directory in (private, private / "gnupg", private / "pass"):
        if directory.is_symlink():
            raise ValueError("keychain directory is symlinked")
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        if directory.stat().st_mode & 0o077 or directory.stat().st_uid != os.getuid():
            raise ValueError("keychain directory permissions differ")
    if (private / "pass/.gpg-id").is_file():
        return
    env = environment(binding)
    public = subprocess.run([binding["gpg"], "--batch", "--with-colons", "--list-secret-keys"],
                            env=env, capture_output=True, text=True, check=True)
    fingerprints = [line.split(":")[9] for line in public.stdout.splitlines() if line.startswith("fpr:")]
    if not fingerprints:
        subprocess.run([binding["gpg"], "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
                        "--quick-generate-key", "Loom Tyler Mail <tyler-mail@localhost.invalid>",
                        "default", "default", "0"], env=env, check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        public = subprocess.run([binding["gpg"], "--batch", "--with-colons", "--list-secret-keys"],
                                env=env, capture_output=True, text=True, check=True)
        fingerprints = [line.split(":")[9] for line in public.stdout.splitlines() if line.startswith("fpr:")]
    if not fingerprints:
        raise ValueError("private credential key was not created")
    subprocess.run([binding["pass"], "init", fingerprints[0]], env=env,
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def client_args(args):
    args = list(args)
    if any(arg in ("--config", "-c") or arg.startswith(("--config=", "-c")) for arg in args):
        raise ValueError("use the selected Tyler mail configuration")
    for index in range(len(args) - 1):
        if args[index:index + 2] == ["message", "read"] and "--preview" not in args and "-p" not in args:
            args.insert(index + 2, "--preview")
            break
    return args


def check_client(binding):
    env = environment(binding)
    env["SSL_CERT_FILE"] = str(certificate(binding))
    check_clients(binding, env)


def configure(binding):
    address, display_name = sender_identity(binding)
    if not sys.stdin.isatty():
        raise ValueError("configure mail in your own terminal; never send a password through an agent")
    config = Path(binding["environment"]["XDG_CONFIG_HOME"]) / "himalaya/config.toml"
    if config.exists() or config.is_symlink():
        info = config.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("mail configuration custody differs; preserve it and request review")
        prepare_certificate(binding, control, environment(binding))
        prepare_sender(binding)
        check_client(binding)
        print("Existing credentials/config preserved; native IMAP/SMTP authentication checks passed. No mail sent.")
        return
    if (Path(binding["private"]) / "pass/loom-tyler-proton-client.gpg").exists():
        raise ValueError("mail credential already exists; preserve it and request review")
    control(binding, "is-active", capture_output=True)
    cert = prepare_certificate(binding, control, environment(binding))
    username = input("Bridge IMAP username from info: ").strip()
    if not username:
        raise ValueError("explicit Bridge username required")
    if len(username) > 256 or any(ord(char) < 32 for char in username):
        raise ValueError("invalid Bridge username")
    password = getpass.getpass("Local Bridge password from info (NOT your Proton password): ")
    if not password or "\n" in password or "\r" in password:
        raise ValueError("invalid local Bridge password")
    client = imaplib.IMAP4("127.0.0.1", 1143, timeout=15)
    try:
        client.starttls(ssl.create_default_context(cafile=str(cert)))
        client.login(username, password)
        client.noop()
    finally:
        client.logout()
    subprocess.run([binding["pass"], "insert", "--multiline", "loom-tyler-proton-client"],
                   input=password + "\n", text=True, env=environment(binding), check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    secret = shlex.join([binding["pass"], "show", "loom-tyler-proton-client"])
    text = ('[accounts.tyler]\nemail = ' + json.dumps(address) + '\ndisplay-name = ' + json.dumps(display_name) + '\ndefault = true\n'
            'backend.type = "imap"\nbackend.host = "127.0.0.1"\nbackend.port = 1143\n'
            'backend.encryption.type = "start-tls"\nbackend.auth.type = "password"\n'
            'backend.login = ' + json.dumps(username) + '\nbackend.auth.cmd = ' + json.dumps(secret) + '\n'
            'message.send.backend.type = "smtp"\nmessage.send.backend.host = "127.0.0.1"\n'
            'message.send.backend.port = 1025\nmessage.send.backend.encryption.type = "start-tls"\n'
            'message.send.backend.auth.type = "password"\nmessage.send.backend.login = ' + json.dumps(username) + '\n'
            'message.send.backend.auth.cmd = ' + json.dumps(secret) + '\n')
    config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with os.fdopen(os.open(config, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "w") as stream:
        stream.write(text)
        stream.flush()
        os.fsync(stream.fileno())
    prepare_sender(binding)
    check_client(binding)
    print("Native IMAP/SMTP authentication verified; private client configuration saved. No messages read or sent.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("command", choices=("status", "login", "configure", "prepare-client", "check-client", "start", "stop", "serve", "client"))
    parser.add_argument("args", nargs=argparse.REMAINDER)
    options = parser.parse_args()
    binding = json.loads(options.binding.read_bytes())
    os.umask(0o077)
    check_profile(binding)
    env = environment(binding)
    if options.command == "status":
        result = control(binding, "is-active", check=False, capture_output=True)
        print(json.dumps(dict(service=result.stdout.strip(), priority_address=binding["priority_address"],
                              configured_keychain=(Path(binding["private"]) / "pass/.gpg-id").is_file(),
                              configured_client=(Path(env["XDG_CONFIG_HOME"]) / "himalaya/config.toml").is_file())))
        return
    if options.command in ("start", "stop"):
        control(binding, options.command)
        return
    if options.command == "serve":
        os.chdir(binding["home"])
        os.execve(binding["bridge"], [binding["bridge"], "--noninteractive"], env)
    if options.command == "configure":
        configure(binding)
        return
    if options.command == "prepare-client":
        control(binding, "is-active", capture_output=True)
        path = prepare_certificate(binding, control, env)
        certificate(binding)
        prepare_sender(binding)
        print(json.dumps(dict(verified_local_tls=True, certificate_sha256=hashlib.sha256(path.read_bytes()).hexdigest(),
                              authenticated=False)))
        return
    if options.command == "check-client":
        check_client(binding)
        print(json.dumps(dict(native_imap_verified=True, native_smtp_tls_verified=True,
                              smtp_auth_noop_verified=True, delivery_tested=False, messages_sent=0)))
        return
    if options.command == "login":
        if not sys.stdin.isatty():
            raise ValueError("run login in your own interactive SSH terminal; never send passwords through an agent")
        init_keychain(binding)
        control(binding, "stop")
        print("Proton Bridge: enter login, then your account/password/2FA privately.")
        print("Keep combined-address mode for the authorized whole mailbox; existing filters stay unchanged.")
        print("Use info to see the local mail-client settings. After exit, the Main service starts again.")
        try:
            result = subprocess.run([binding["bridge"], "--cli"], env=env, cwd=binding["home"])
        finally:
            control(binding, "start")
        raise SystemExit(result.returncode)
    os.chdir(binding["home"])
    cert = Path(binding["private"]) / "bridge-cert.pem"
    if cert.is_file():
        env["SSL_CERT_FILE"] = str(cert)
    os.execve(binding["himalaya"], [binding["himalaya"], *client_args(options.args)], env)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        detail = str(error) if isinstance(error, ValueError) else type(error).__name__
        print("Tyler mail unavailable: " + detail, file=sys.stderr)
        raise SystemExit(69)
