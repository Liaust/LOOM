import copy
from datetime import datetime, timezone
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("reconciliation_dispatch", Path(__file__).with_name("reconciliation_dispatch.py"))
dispatch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dispatch)


def envelope(values, status="completed"):
    return {"ok": True, "data": {"status": status, "job_id": "job-real",
            "capability_call": {"capability_call_id": "call-real"},
            "result": {"job": {"job": {"job_id": "job-real", "status": status},
            "outputs": [{"output_key": k, "value_json": v} for k, v in values.items()]}}}}


class ReceiptTest(unittest.TestCase):
    def test_preserves_two_minute_late_window_without_review_dispatch(self):
        execution = {"id": "occurrence", "scheduled_instant": "2026-10-19T03:00:00Z"}
        for second in (0, 119, 120):
            now = datetime.fromtimestamp(datetime(2026, 10, 19, 3, tzinfo=timezone.utc).timestamp() + second, timezone.utc)
            self.assertIsNone(dispatch.late_receipt(execution, "native", now))
        skipped = dispatch.late_receipt(execution, "native", datetime(2026, 10, 19, 3, 2, 1, tzinfo=timezone.utc))
        self.assertEqual(skipped["review_status"], "skipped_late")
        self.assertFalse(skipped["agent_started"])
        self.assertNotIn("job_id", skipped)

    def test_missing_or_unknown_occurrence_time_does_not_dispatch(self):
        for scheduled in (None, "2026-10-19T03:00:00", "2026-10-20T03:00:00Z"):
            with self.assertRaises(ValueError):
                dispatch.late_receipt({"id": "occurrence", "scheduled_instant": scheduled}, "native", datetime(2026, 10, 19, 3, 0, 30, tzinfo=timezone.utc))

    def test_source_must_be_exact_and_inactive(self):
        for status in ("active", None, "running"):
            self.assertFalse(dispatch.source_paused({"ok": True, "data": {"schedule": {"schedule_id": "schedule_01M441KMNAG13G0PSYCR49X2GH", "status": status}}}))
        self.assertFalse(dispatch.source_paused({}))
        self.assertFalse(dispatch.source_paused({"ok": True, "data": {"schedule": {"schedule_id": "different", "status": "paused"}}}))
        self.assertTrue(dispatch.source_paused({"ok": True, "data": {"schedule": {"schedule_id": "schedule_01M441KMNAG13G0PSYCR49X2GH", "status": "paused"}}}))

    def test_no_work_is_explicit_success_without_a_thread(self):
        saved, ok = dispatch.result_receipt(envelope({"review_status": "no_work", "selected_count": 0, "agent_started": False}), "native", "occurrence")
        self.assertTrue(ok)
        self.assertIsNone(saved["thread_id"])
        self.assertEqual(saved["job_id"], "job-real")

    def test_actual_review_requires_completed_saved_thread(self):
        values = {"review_status": "agent_result", "selected_count": 2, "outcome": "completed", "thread_id": "saved-thread"}
        self.assertTrue(dispatch.result_receipt(envelope(values), "native", "occurrence")[1])
        for field, value in (("outcome", "needs_input"), ("thread_id", None), ("review_status", "gate_failed")):
            changed = copy.deepcopy(values)
            changed[field] = value
            self.assertFalse(dispatch.result_receipt(envelope(changed), "native", "occurrence")[1])

    def test_queued_failed_or_incomplete_is_not_success(self):
        values = {"review_status": "no_work", "selected_count": 0, "agent_started": False}
        for status in ("queued", "running", "failed"):
            self.assertFalse(dispatch.result_receipt(envelope(values, status), "native", "occurrence")[1])
        self.assertFalse(dispatch.result_receipt({}, "native", "occurrence")[1])


if __name__ == "__main__":
    unittest.main()
