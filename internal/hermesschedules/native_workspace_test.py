import contextlib
import copy
import json
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch
from native_project_test import ProjectOwnerTest
import native_workspace as workspace
import native_project as owner
import native_boxes as boxes


class WorkspaceControlsTest(unittest.TestCase):
    def setUp(self):
        ProjectOwnerTest.setUp(self)
        self.enterContext(patch.object(workspace, "profile_environment", lambda home: contextlib.nullcontext()))
        self.enterContext(patch.dict(sys.modules, {"hermes_time": types.SimpleNamespace(get_timezone_name=lambda: "Europe/Amsterdam")}))
        original = self.native.create_job
        def create(**kw):
            kw.setdefault("skills", [])
            result=original(**kw)
            if kw.get("paused"):
                result=self.native.update_job(result["id"],dict(enabled=False,state="paused",next_run_at=None))
            return result
        self.native.create_job=create
        self.native.resume_job=lambda ref:self.native.update_job(ref,dict(enabled=True,state="scheduled",paused_at=None,next_run_at="2099-01-01T00:00:00Z"))

    def call(self, **changes):
        request=dict(operation="workspace_create",profile="mina",token="workspace-create-0001",spec=dict(name="Review",prompt="Only read project status",schedule="0 9 * * 1"))
        request.update(changes)
        return workspace.execute(self.native,self.home,request,lambda:None)

    def test_workspace_atomic_create_cas_replay_and_declared_refusal(self):
        first=self.call()["job"]
        self.assertTrue(first["paused"])
        self.assertEqual(self.writes,1)
        self.assertEqual(self.call()["job"]["id"],first["id"])
        paused=copy.deepcopy(first)
        active=self.call(operation="workspace_resume",id=first["id"],expected_revision=first["revision"],token="workspace-resume-0001",spec=None)["job"]
        self.assertFalse(active["paused"])
        # A creation replay after another operation must never create a duplicate.
        self.assertEqual(self.call()["job"]["id"],first["id"])
        self.assertEqual(len(owner.strict_jobs(self.home)),2)
        with self.assertRaises(owner.Conflict):
            self.call(operation="workspace_pause",id=first["id"],expected_revision=paused["revision"],token="workspace-stale-0001",spec=None)
        jobs=owner.strict_jobs(self.home);jobs[-1]["origin"][owner.MARKER]={"project_id":"project_test"};self.path.write_text(json.dumps({"jobs":jobs}))
        with self.assertRaises(owner.Conflict):
            self.call(operation="workspace_pause",id=first["id"],expected_revision=workspace.revision(jobs[-1]),token="workspace-pause-0001",spec=None)

    def test_workspace_rejects_other_profile_extra_fields_and_changed_token_payload(self):
        with self.assertRaises(ValueError):self.call(profile="other")
        with self.assertRaises(ValueError):self.call(workdir="/tmp")
        self.call()
        with self.assertRaises(owner.Conflict):self.call(spec=dict(name="Other",prompt="Other",schedule="0 9 * * 1"))

    def test_old_resume_cannot_undo_later_pause_or_revive_project_events(self):
        first=self.call()["job"]
        request=dict(operation="workspace_resume",id=first["id"],expected_revision=first["revision"],token="workspace-resume-0001",spec=None)
        active=self.call(**request)["job"]
        self.call(operation="workspace_pause",id=first["id"],expected_revision=active["revision"],token="workspace-pause-0001",spec=None)
        with self.assertRaises(owner.Conflict):self.call(**request)
        jobs=owner.strict_jobs(self.home);jobs[-1]["origin"]["loom_internal_events"]={"project_id":"project_test","retired":True};self.path.write_text(json.dumps({"jobs":jobs}))
        self.assertTrue(workspace.projection(jobs[-1],True)["managed"])
        with self.assertRaises(owner.Conflict):self.call(operation="workspace_resume",id=first["id"],expected_revision=workspace.revision(jobs[-1]),token="workspace-events-0001",spec=None)

    def test_edit_once_preserves_cumulative_history_and_late_resume_is_not_committed(self):
        first=self.call()["job"]
        jobs=owner.strict_jobs(self.home);jobs[-1]["repeat"]["completed"]=7;self.path.write_text(json.dumps({"jobs":jobs}))
        result=self.call(operation="workspace_edit",id=first["id"],expected_revision=workspace.revision(jobs[-1]),token="workspace-edit-0001",spec=dict(name="One",prompt="Read only",schedule="2099-01-01T00:00:00Z"))["job"]
        self.assertEqual(owner.strict_jobs(self.home)[-1]["repeat"],{"times":8,"completed":7})
        before=self.path.read_bytes();self.native.resume_job=lambda ref:self.native.update_job(ref,dict(enabled=True,state="scheduled",next_run_at="2020-01-01T00:00:00Z"))
        with self.assertRaises(owner.Conflict):self.call(operation="workspace_resume",id=first["id"],expected_revision=result["revision"],token="workspace-late-0001",spec=None)
        self.assertEqual(self.path.read_bytes(),before)

    def company(self):
        # JSON is valid YAML; keep this resolver fixture independent of the
        # installed Hermes YAML package and all private runtime configuration.
        self.enterContext(patch.dict(sys.modules, {"hermes_yaml": types.SimpleNamespace(safe_load=json.loads, YAMLError=ValueError)}))
        root = self.home.resolve() / "company"
        (root / ".loom").mkdir(parents=True)
        (root / ".loom/box.yaml").write_text(json.dumps(dict(schema_version="loom.box.v0.5", box_id="box_company", owner_node="main", root_path=str(root))))
        self.company_home = self.home / "profiles/tyler"
        (self.company_home / "cron").mkdir(parents=True)
        self.company_path = self.company_home / "cron/jobs.json"
        self.company_path.write_text(json.dumps({"jobs": [{"id": "manual", "name": "Private Tyler routine", "prompt": "company only"}]}))
        self.box = dict(schema_version="loom.box_agent.v1", box_id="box_company", box_key="company", profile="tyler", workdir=str(root), context_revision=owner.digest("company context"), retired=False)
        (self.company_home / boxes.MARKER).write_text(json.dumps(self.box))
        (self.company_home / "SOUL.md").write_text("Test fixture only.\n")
        (self.company_home / "config.yaml").write_text(json.dumps(dict(terminal=dict(cwd=str(root)), profile_context=dict(required_cwd=str(root), allow_root_auth_fallback=False))))
        self.catalog = self.home / "boxes.json"
        self.catalog.write_text(json.dumps(dict(schema_version="loom.box_catalog.v1", boxes=[dict(key="company", root_path=str(root), node="main")])))
        self.active_home = None
        @contextlib.contextmanager
        def environment(home):
            previous = self.active_home
            self.active_home = Path(home)
            try:
                yield
            finally:
                self.active_home = previous
        self.enterContext(patch.object(workspace, "profile_environment", environment))
        self.enterContext(patch.dict(sys.modules, {"hermes_time": types.SimpleNamespace(get_timezone_name=lambda: "America/Sao_Paulo" if self.active_home == self.company_home else "Europe/Amsterdam")}))
        @contextlib.contextmanager
        def store(home):
            previous = self.home, self.path
            self.home, self.path = Path(home), Path(home) / "cron/jobs.json"
            try:
                self.assertEqual(self.active_home, Path(home))
                yield
            finally:
                self.home, self.path = previous
        self.native.use_cron_store = store
        parse = self.native.parse_schedule
        def parse_in_profile(expr):
            self.assertEqual(self.active_home, self.company_home)
            return parse(expr)
        self.native.parse_schedule = parse_in_profile

    def company_call(self, **changes):
        source = dict(self.source, revision=owner.PLATFORM_REVISION)
        request = dict(operation="workspace_create", source=source, box_id="box_company", profile="tyler", token="company-create-0001", spec=dict(name="Review", prompt="Only company status", schedule="0 9 * * 1"))
        request.update(changes)
        return owner.execute(self.native, self.home, source, request, boxes_file=str(self.catalog))

    def test_company_info_discovers_exact_profile_and_selects_local_detail(self):
        self.company()
        with patch.dict(os.environ, {"HOME": "/not-the-owner", "HERMES_HOME": "/not-the-catalog"}):
            info = self.company_call(operation="workspace_info", profile="", token="", spec=None)
        self.assertEqual(info["profile"], "tyler")
        self.assertEqual(info["box_id"], "box_company")
        self.assertEqual(info["timezone"], "America/Sao_Paulo")
        self.assertEqual(info["profiles"], [dict(id="tyler", name="Tyler", timezone="America/Sao_Paulo", box_id="box_company")])
        self.assertEqual(len(info["items"]), 1)
        item = info["items"][0]
        self.assertEqual((item["id"], item["profile"], item["box_id"], item["name"]), ("manual", "tyler", "box_company", "Tyler routine manual"))
        self.assertNotIn("prompt", item)
        detail = self.company_call(operation="workspace_detail", id="manual", token="", spec=None)["job"]
        self.assertEqual(detail["prompt"], "company only")
        self.assertTrue(detail["editable"])
        self.assertEqual(owner.strict_jobs(self.home)[0]["prompt"], "private")
        self.assertEqual(self.writes, 0)
        self.assertIsNone(self.active_home)
        source = dict(self.source, revision=owner.PLATFORM_REVISION)
        self.assertEqual(owner.execute(self.native, self.home, source, dict(operation="workspace_info", source=source, box_id="box_company"), boxes_file=str(self.catalog))["profile"], "tyler")

    def test_company_paused_create_context_revision_replay_and_future_resume(self):
        self.company()
        personal = self.path.read_bytes()
        first = self.company_call()["job"]
        self.assertTrue(first["paused"])
        self.assertEqual((first["profile"], first["box_id"]), ("tyler", "box_company"))
        self.assertEqual(self.writes, 1)
        context = {k: self.box[k] for k in workspace.CONTEXT_FIELDS}
        stored = owner.strict_jobs(self.company_home)[-1]
        self.assertEqual(stored["workdir"], self.box["workdir"])
        self.assertEqual(stored["origin"][workspace.BOX_CONTEXT], context)
        self.assertEqual(self.company_call()["job"], first)
        self.assertEqual(self.writes, 1)
        with self.assertRaises(owner.Conflict):
            self.company_call(spec=dict(name="Changed", prompt="Changed", schedule="0 9 * * 1"))
        edited = self.company_call(operation="workspace_edit", id=first["id"], expected_revision=first["revision"], token="company-edit-0001", spec=dict(name="Changed", prompt="Changed", schedule="every 60m"))["job"]
        self.assertNotEqual(edited["revision"], first["revision"])
        self.assertTrue(edited["paused"])
        self.assertEqual(owner.strict_jobs(self.company_home)[-1]["origin"][workspace.BOX_CONTEXT], context)
        with self.assertRaises(owner.Conflict):
            self.company_call(operation="workspace_pause", id=first["id"], expected_revision=first["revision"], token="company-stale-0001", spec=None)
        resume = dict(operation="workspace_resume", id=first["id"], expected_revision=edited["revision"], token="company-resume-0001", spec=None)
        active = self.company_call(**resume)["job"]
        self.assertFalse(active["paused"])
        self.assertEqual(active["next_run_at"], "2099-01-01T00:00:00Z")
        self.assertEqual(self.company_call(**resume)["job"], active)
        retained = self.company_call(operation="workspace_pause", id=first["id"], expected_revision=active["revision"], token="company-pause-0001", spec=None)["job"]
        with self.assertRaises(owner.Conflict):
            self.company_call(**resume)
        self.assertEqual(self.company_call()["job"], retained)
        self.assertEqual(len(owner.strict_jobs(self.company_home)), 2)
        self.assertEqual(self.path.read_bytes(), personal)

    def test_company_selection_cross_box_id_and_home_overrides_refuse(self):
        self.company()
        self.path.write_text(json.dumps({"jobs": [{"id": "personal-only", "prompt": "private"}, {"id": "manual", "prompt": "private"}]}))
        personal, company = self.path.read_bytes(), self.company_path.read_bytes()
        for changes in (
            dict(box_id=""), dict(box_id="box_other"), dict(profile="mina"),
            dict(operation="workspace_detail", profile="", id="manual"),
            dict(profile=""), dict(home=str(self.company_home)),
            dict(boxes_file=str(self.catalog)), dict(workdir=self.box["workdir"]),
            dict(operation="workspace_detail", id="personal-only"),
            dict(operation="workspace_pause", id="manual", expected_revision=workspace.revision(owner.strict_jobs(self.home)[-1])),
            dict(source=dict(self.source, profile="tyler", revision=owner.PLATFORM_REVISION)),
        ):
            with self.subTest(changes=changes), self.assertRaises((owner.Conflict, ValueError)):
                self.company_call(**changes)
        self.assertEqual(self.path.read_bytes(), personal)
        self.assertEqual(self.company_path.read_bytes(), company)
        self.assertEqual(self.writes, 0)
        # Even byte-identical legacy jobs in another store cannot supply a
        # Company CAS revision; ownership comes from selection, not the title.
        self.path.write_bytes(company)
        native = owner.strict_jobs(self.company_home)[0]
        selected = self.company_call(operation="workspace_detail", id="manual")["job"]
        self.assertNotEqual(selected["revision"], workspace.revision(native))
        self.assertNotEqual(selected["revision"], workspace.revision(native, dict(self.box, box_id="box_other", profile="other")))
        with self.assertRaises(owner.Conflict):
            self.company_call(operation="workspace_pause", id="manual", expected_revision=workspace.revision(native))
        self.assertEqual(self.company_path.read_bytes(), company)

    def test_company_discovery_ambiguous_retired_or_unverified_profiles_refuse(self):
        self.company()
        marker = self.company_home / boxes.MARKER
        for change in (dict(retired=True), dict(box_key="unknown")):
            marker.write_text(json.dumps(dict(self.box, **change)))
            with self.assertRaises(owner.Conflict):
                self.company_call(operation="workspace_info", profile="")
        marker.write_text(json.dumps(self.box))
        with patch.object(boxes, "resolve", wraps=boxes.resolve) as resolve:
            extra = self.home / "profiles/other"
            extra.mkdir()
            (extra / boxes.MARKER).write_text(json.dumps(dict(self.box, profile="other")))
            with self.assertRaises(owner.Conflict):
                self.company_call(operation="workspace_info", profile="")
            resolve.assert_not_called()
        with self.assertRaises(owner.Conflict):
            self.company_call(operation="workspace_info", box_id="box_missing", profile="")
        with self.assertRaises(ValueError):
            workspace.execute(self.native, self.home, dict(operation="workspace_info", box_id="box_company", profile="tyler"), lambda: None)
        descriptor = Path(self.box["workdir"]) / ".loom/box.yaml"
        value = json.loads(descriptor.read_text()); value["box_id"] = "box_changed"
        descriptor.write_text(json.dumps(value))
        with self.assertRaises(owner.Conflict):
            self.company_call(operation="workspace_info")
        self.assertEqual(self.writes, 0)

    def test_company_context_drift_and_managed_terminal_edits_refuse(self):
        self.company()
        first = self.company_call()["job"]
        original = owner.strict_jobs(self.company_home)
        changed = copy.deepcopy(original)
        changed[-1]["origin"][workspace.BOX_CONTEXT]["box_id"] = "box_other"
        self.assertNotEqual(workspace.revision(changed[-1]), first["revision"])
        for job_changes in (
            dict(origin=changed[-1]["origin"]),
            dict(origin=dict(original[-1]["origin"], **{owner.MARKER: dict(project_id="project_test")})),
            dict(origin=dict(original[-1]["origin"], **{owner.BOX_MARKER: dict(box_id="box_company")})),
            dict(origin=dict(original[-1]["origin"], loom_internal_events=dict(project_id="project_test"))),
            dict(state="completed"),
        ):
            current = copy.deepcopy(original)
            current[-1].update(job_changes)
            self.company_path.write_text(json.dumps({"jobs": current}))
            before = self.company_path.read_bytes()
            with self.subTest(changes=job_changes), self.assertRaises(owner.Conflict):
                self.company_call(operation="workspace_edit", id=first["id"], expected_revision=workspace.revision(current[-1]), token="company-refused-0001")
            self.assertEqual(self.company_path.read_bytes(), before)

    def test_personal_empty_box_preserves_legacy_revision_and_replay(self):
        first = self.call()["job"]
        self.assertEqual(self.call(box_id="")["job"], first)
        job = owner.strict_jobs(self.home)[-1]
        self.assertEqual(first["revision"], owner.digest(dict(id=job["id"], name=job.get("name"), config=owner.configuration(job), paused=owner.paused(job), terminal=owner.terminal(job), control_generation=job["origin"][workspace.MARKER]["token"])))
        self.assertNotIn(workspace.BOX_CONTEXT, job["origin"])
        self.assertEqual(job["workdir"], str(self.home.parent))
        info = self.call(operation="workspace_info", token="", spec=None)
        self.assertEqual((info["profile"], info["box_id"]), ("mina", ""))
        self.assertEqual(info["profiles"], [dict(id="mina", name="MINA", timezone="Europe/Amsterdam", box_id="")])
        self.assertTrue(all(j["profile"] == "mina" for j in info["items"]))
        with self.assertRaises(ValueError):
            self.call(operation="workspace_info", profile="")

if __name__=="__main__":unittest.main()
