"""Explicit task/report submission to the existing native Codex App Server."""

import argparse
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import sys
import time
import uuid

MAX_MESSAGE = 64 * 1024


class DeliveryError(Exception):
    def __init__(self, code, unknown=False):
        super().__init__(code)
        self.unknown = unknown


def exact_thread(value):
    try:
        if str(uuid.UUID(value)) == value:
            return value
    except (ValueError, TypeError, AttributeError):
        pass
    raise ValueError("exact native thread UUID required")


def turn_params(kind, thread, sender, authorization, message):
    exact_thread(thread)
    exact_thread(sender)
    if kind not in ("task", "report"):
        raise ValueError("explicit task or report kind required")
    if not isinstance(message, str) or not message.strip() or len(message.encode()) > MAX_MESSAGE:
        raise ValueError("nonempty message of at most 64KiB required")
    if kind == "task":
        if (not authorization or len(authorization) > 512
                or any(ord(c) < 32 or ord(c) == 127 for c in authorization)):
            raise ValueError("task requires a bounded non-secret authorization reference")
        text = (f"Authorised delegated task from thread {sender}.\n"
                f"Human authorization reference (caller supplied): {authorization}\n"
                "This assignment is relayed by an agent, not personally typed by the human. "
                "The reference records existing authority; it grants no new permissions.\n\n" + message)
        return {"threadId": thread, "input": [{"type": "text", "text": text}]}
    if authorization:
        raise ValueError("reports carry no new task authorization")
    return {"threadId": thread, "input": [], "toolOutput": {
        "name": "delegation_report", "namespace": "loom",
        "output": f"Report from thread {sender} (information, not a new assignment).\n\n" + message}}


def idle_target(call, thread):
    value = call("thread/read", {"threadId": thread, "includeTurns": False})["thread"]
    if value.get("id") != thread or value.get("status", {}).get("type") not in ("idle", "notLoaded"):
        raise DeliveryError("target_busy_or_unverified")
    if value["status"]["type"] == "idle":
        for method in ("thread/queue/list", "thread/backgroundTerminals/list"):
            pending = call(method, {"threadId": thread, "limit": 1})
            if pending.get("data") != [] or pending.get("nextCursor"):
                raise DeliveryError("target_has_pending_work")


def deliver(call, params):
    thread = params["threadId"]
    idle_target(call, thread)
    call("thread/resume", {"threadId": thread, "excludeTurns": True})
    idle_target(call, thread)
    # Never retry after this boundary: loss of the reply does not prove rejection.
    try:
        result = call("turn/start", params)
        turn = result.get("turn", {})
        if not turn.get("id"):
            raise ValueError("missing accepted turn")
    except Exception as error:
        raise DeliveryError("delivery_unknown_do_not_resend", unknown=True) from error
    return {"status": "submitted", "thread_id": thread, "turn_id": turn["id"],
            "kind": "report" if "toolOutput" in params else "task",
            "native_item_type": "functionCallOutput" if "toolOutput" in params else "userMessage",
            "completion": "not_observed"}


@contextmanager
def runtime_lease(home):
    with (home / "loom-runtime-update.lock").open("a") as lease:
        try:
            fcntl.flock(lease, fcntl.LOCK_SH | fcntl.LOCK_NB)
        except BlockingIOError:
            raise DeliveryError("runtime_update_in_progress") from None
        yield


class NativeRPC:
    def __init__(self, connection):
        self.connection = connection
        self.sequence = 0

    def call(self, method, params):
        self.sequence += 1
        self.connection.send(json.dumps({"id": self.sequence, "method": method, "params": params}))
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            message = json.loads(self.connection.recv(timeout=max(0.1, deadline-time.monotonic())))
            if message.get("id") == self.sequence:
                if "error" in message or not isinstance(message.get("result"), dict):
                    raise DeliveryError("native_request_refused:" + method)
                return message["result"]
        raise DeliveryError("native_request_timeout:" + method)


def main():
    parser = argparse.ArgumentParser(description=__doc__, epilog="Requires existing human authority. Submission is not completion. No automatic delivery retry.")
    parser.add_argument("kind", choices=("task", "report"))
    parser.add_argument("thread_id")
    parser.add_argument("--from-thread", required=True, help="exact sending thread UUID for attribution")
    parser.add_argument("--authorization-ref", help="task-only reference to the human's existing authorization, not a grant")
    args = parser.parse_args()
    result = None
    try:
        raw = sys.stdin.buffer.read(MAX_MESSAGE + 1)
        if len(raw) > MAX_MESSAGE:
            raise ValueError("message exceeds 64KiB")
        params = turn_params(args.kind, args.thread_id, args.from_thread, args.authorization_ref, raw.decode("utf-8"))
        home = Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex")))
        from websockets.sync.client import unix_connect
        with runtime_lease(home), unix_connect(str(home / "app-server-control/app-server-control.sock"),
                open_timeout=5, close_timeout=1, max_size=2*1024*1024) as connection:
            rpc = NativeRPC(connection)
            rpc.call("initialize", {"clientInfo": {"name": "loom-codex-message", "version": "1"},
                                    "capabilities": {"experimentalApi": True}})
            connection.send(json.dumps({"method": "initialized"}))
            result = deliver(rpc.call, params)
        print(json.dumps(result))
        return 0
    except (ValueError, UnicodeError) as error:
        print(json.dumps({"status": "not_sent", "error": str(error)}))
    except DeliveryError as error:
        print(json.dumps({"status": "unknown" if error.unknown else "not_sent", "error": str(error)}))
    except Exception as error:
        if result is not None:
            result["warning"] = "receipt_received_connection_close_failed"
            print(json.dumps(result))
            return 0
        print(json.dumps({"status": "not_sent", "error": "native_unavailable:" + type(error).__name__}))
    return 1


if __name__ == "__main__":
    sys.exit(main())
