import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("delegation", Path(__file__).with_name("delegation.py"))
sender = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sender)
THREAD = "00000000-0000-4000-8000-000000000004"
SOURCE = "00000000-0000-4000-8000-000000000005"


class DeliveryTest(unittest.TestCase):
    def test_explicit_modes_and_no_settings_overrides(self):
        task = sender.turn_params("task", THREAD, SOURCE, "the operator's exact request", "Read only.")
        self.assertEqual(set(task), {"threadId", "input"})
        self.assertIn(SOURCE, task["input"][0]["text"])
        self.assertIn("not personally typed", task["input"][0]["text"])
        report = sender.turn_params("report", THREAD, SOURCE, None, "Evidence received.")
        self.assertEqual(report["input"], [])
        self.assertEqual(report["toolOutput"]["name"], "delegation_report")
        self.assertNotIn("Read only.", report["toolOutput"]["output"])
        self.assertNotIn("authorization", report["toolOutput"])

    def test_required_authority_and_bounded_identity_input(self):
        for args in [("task", THREAD, SOURCE, None, "text"), ("report", THREAD, SOURCE, "grant", "text"),
                     ("auto", THREAD, SOURCE, None, "text"), ("task", "bad", SOURCE, "ref", "text"),
                     ("task", THREAD, "bad", "ref", "text"), ("task", THREAD, SOURCE, "bad\nref", "text"),
                     ("task", THREAD, SOURCE, "ref", ""), ("report", THREAD, SOURCE, None, "a"*65537)]:
            with self.subTest(args=args[:4]), self.assertRaises(ValueError):
                sender.turn_params(*args)

    def test_single_submission_after_metadata_only_resume(self):
        calls = []
        def rpc(method, params):
            calls.append((method, params))
            if method == "thread/read": return {"thread": {"id": THREAD, "status": {"type": "idle"}}}
            if method == "thread/resume": return {"thread": {"id": THREAD}}
            if method == "turn/start": return {"turn": {"id": "accepted"}}
            return {"data": [], "nextCursor": None}
        params = sender.turn_params("task", THREAD, SOURCE, "ref", "Do the task.")
        result = sender.deliver(rpc, params)
        self.assertEqual(result["turn_id"], "accepted")
        self.assertEqual(result["native_item_type"], "userMessage")
        self.assertEqual([p for m,p in calls if m == "thread/resume"], [{"threadId": THREAD, "excludeTurns": True}])
        self.assertEqual([p for m,p in calls if m == "turn/start"], [params])

    def test_busy_and_late_busy_never_interrupt_or_submit(self):
        for late in [False, True]:
            calls = []
            def rpc(method, params):
                calls.append(method)
                if method == "thread/read":
                    state = "active" if not late or "thread/resume" in calls else "idle"
                    return {"thread": {"id": THREAD, "status": {"type": state}}}
                return {"data": []}
            with self.subTest(late=late), self.assertRaises(sender.DeliveryError):
                sender.deliver(rpc, {"threadId": THREAD})
            self.assertNotIn("turn/start", calls)
            self.assertNotIn("turn/interrupt", calls)

    def test_unknown_send_is_not_retried(self):
        calls = []
        def rpc(method, params):
            calls.append(method)
            if method == "thread/read": return {"thread": {"id": THREAD, "status": {"type": "idle"}}}
            if method == "turn/start": raise TimeoutError()
            return {"data": []}
        with self.assertRaises(sender.DeliveryError) as caught:
            sender.deliver(rpc, {"threadId": THREAD})
        self.assertTrue(caught.exception.unknown)
        self.assertEqual(calls.count("turn/start"), 1)


if __name__ == "__main__":
    unittest.main()
