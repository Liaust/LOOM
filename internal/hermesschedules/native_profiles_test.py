"""Small real-native API check; run with the installed Hermes Python on Main."""
import copy
import json
import os
from pathlib import Path
import tempfile
import unittest

import native_project as owner
import native_profiles as profiles


class NativeProfilesTest(unittest.TestCase):
    def test_profile_replay_drift_schedule_and_archive(self):
        try:
            from hermes_cli import profiles as native_profiles
            from cron import jobs
        except ImportError:
            self.skipTest("requires installed native Hermes")
        with tempfile.TemporaryDirectory(prefix="loom-hermes-profile-") as directory:
            home = Path(directory).resolve()
            directory = str(home)
            old_home = os.environ.get("HERMES_HOME")
            os.environ["HERMES_HOME"] = directory
            self.addCleanup(lambda: os.environ.pop("HERMES_HOME", None) if old_home is None else os.environ.__setitem__("HERMES_HOME", old_home))
            (home / ".env").write_text("LOOM_TEST_ROOT_SECRET=do-not-clone\n")
            source = dict(host="main", profile="mina", revision=owner.PLATFORM_REVISION)
            request = dict(operation="reconcile_agent", source=source, project_id="project_test", resource="researcher", expected_revision="absent", expected_id="", token="first", input_hash=owner.digest("first"), agent=dict(instructions="Research only this project's notes.\n", model="gemma3:4b", provider="ollama", workdir=directory))
            first = owner.execute(jobs, home, source, request)[0]
            profile = home / "profiles" / first["id"]
            self.assertEqual(profile, native_profiles.get_profile_dir(first["id"]))
            self.assertNotIn("do-not-clone", (profile / ".env").read_text())
            self.assertFalse((profile / "auth.json").exists())
            (profile / "memories/MEMORY.md").write_text("Preserve my memory.\n")
            self.assertEqual(owner.execute(jobs, home, source, request), [first])
            native_profiles.write_profile_meta(profile, display_name="My project reader")
            next_request = dict(request, expected_revision=first["revision"], expected_id=first["id"], token="reapply")
            owner.execute(jobs, home, source, next_request)
            import hermes_yaml as yaml
            self.assertEqual(yaml.safe_load((profile / "profile.yaml").read_text())["display_name"], "My project reader")
            schedule = dict(operation="reconcile", source=source, project_id="project_test", resource="review", expected_revision="absent", expected_id="", token="schedule", input_hash=owner.digest("schedule"), spec=dict(profile=first["id"], schedule="every 60m", prompt="Read notes", skills=[], status="paused", workdir=directory))
            created = owner.execute(jobs, home, source, schedule)[0]
            self.assertEqual(created["profile"], first["id"])
            self.assertTrue(created["paused"])
            codex = copy.deepcopy(schedule)
            codex.update(resource="coding", token="codex", input_hash=owner.digest("codex"))
            codex["spec"]["codex"] = dict(model="gpt-6-astra", timeout_seconds=90, sandbox="read-only")
            dispatched = owner.execute(jobs, home, source, codex)[0]
            script = owner.strict_jobs(profile)[-1]
            self.assertTrue(script["no_agent"])
            self.assertTrue((profile / "scripts" / script["script"]).is_file())
            self.assertEqual(owner.execute(jobs, home, source, codex), [dispatched])
            event = dict(operation="dispatch_event", source=source, project_id="project_test", profile="researcher",
                         event=dict(event_id="event_one", event_type="job.failed", job_id="job_one", project_id="project_test", created_at="2026-10-17T07:00:00Z"))
            admitted = owner.execute(jobs, home, source, event)
            self.assertEqual(admitted["delivery"], "queued_native")
            self.assertEqual(owner.execute(jobs, home, source, event)["delivery"], "duplicate")
            queued = next(j for j in owner.strict_jobs(profile) if j["id"] == admitted["native_job_id"])
            due = queued["schedule"]
            event["event"]["event_id"] = "event_two"
            coalesced = owner.execute(jobs, home, source, event)
            self.assertEqual(coalesced["delivery"], "coalesced")
            self.assertEqual(coalesced["native_job_id"], admitted["native_job_id"])
            queued = next(j for j in owner.strict_jobs(profile) if j["id"] == admitted["native_job_id"])
            self.assertEqual(queued["schedule"], due)
            listed = owner.execute(jobs, home, source, dict(operation="list", source=source, project_id="project_test"))
            self.assertEqual(len(listed), 3)
            owner.execute(jobs, home, source, dict(operation="pause_project", source=source, project_id="project_test"))
            queued = next(j for j in owner.strict_jobs(profile) if j["id"] == admitted["native_job_id"])
            self.assertTrue(owner.paused(queued))
            retained = owner.execute(jobs, home, source, dict(operation="list", source=source, project_id="project_test", resource="review"))[0]
            self.assertTrue(retained["paused"] and retained["retired"])
            (profile / "SOUL.md").write_text("A live user edit.\n")
            request.update(expected_revision=profiles.revision(profile, profiles.read_json(profile / profiles.MARKER_FILE)), expected_id=first["id"], token="changed")
            with self.assertRaises(owner.Conflict):
                owner.execute(jobs, home, source, request)
            self.assertEqual((profile / "SOUL.md").read_text(), "A live user edit.\n")
            self.assertEqual((profile / "memories/MEMORY.md").read_text(), "Preserve my memory.\n")


if __name__ == "__main__":
    unittest.main()
