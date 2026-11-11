"""Profile-local workspace controls through MINA's fixed native owner."""
import copy
import re
from datetime import datetime
from pathlib import Path
from native_project import (strict_jobs, marker, paused, terminal, configuration,
                            digest, locked_native_store, transaction, commit, Conflict,
                            profile_environment)

MARKER = "loom_workspace_schedule"
BOX_CONTEXT = "loom_box_context"
CONTEXT_FIELDS = ("box_id", "profile", "workdir", "context_revision")

def revision(job, box=None):
    origin = job.get("origin") or {}
    value = dict(id=job["id"], name=job.get("name"), config=configuration(job),
                 paused=paused(job), terminal=terminal(job),
                 control_generation=origin.get(MARKER, {}).get("token"))
    if BOX_CONTEXT in origin:
        value["box_context"] = origin[BOX_CONTEXT]
    elif box:
        value["box_context"] = {k: box[k] for k in CONTEXT_FIELDS}
    return digest(value)

def project_managed(job):
    return marker(job) is not None or "loom_internal_events" in (job.get("origin") or {})

def projection(job, detail=False, profile="mina", box=None):
    managed = project_managed(job)
    explicit = (job.get("origin") or {}).get(MARKER, {})
    context = (job.get("origin") or {}).get(BOX_CONTEXT)
    if box and context is not None and (not isinstance(context, dict) or any(context.get(k) != box[k] for k in CONTEXT_FIELDS)):
        raise Conflict()
    name = "MINA" if profile == "mina" else profile.replace("_", " ").replace("-", " ").title()
    value = dict(id=job["id"], profile=profile, revision=revision(job, box),
                 name=job.get("name", "")[:160] if explicit else name + " routine " + job["id"],
                 managed=managed, paused=paused(job), terminal=terminal(job),
                 schedule=job.get("schedule"), next_run_at=job.get("next_run_at"))
    if box:
        value["box_id"] = box["box_id"]
    if detail:
        value.update(name=str(job.get("name") or value["name"])[:160],
                     prompt=str(job.get("prompt") or "")[:32768],
                     editable=not managed and not terminal(job) and not any(job.get(k) for k in ("script", "no_agent", "monitor_script", "monitor_url")))
    return value

def execute(native, home, request, alive, boxes_file=None):
    allowed = {"operation", "source", "profile", "box_id", "id", "expected_revision", "token", "spec"}
    operation = request.get("operation")
    if set(request) - allowed or operation not in ("workspace_info", "workspace_detail", "workspace_create", "workspace_edit", "workspace_pause", "workspace_resume"):
        raise ValueError("invalid workspace request")
    box_id, profile = request.get("box_id", ""), request.get("profile", "")
    from native_boxes import BOX, profiles, resolve
    from native_project import KEY
    discover = operation == "workspace_info" and box_id and profile == ""
    if not isinstance(box_id, str) or not isinstance(profile, str) or (not KEY.fullmatch(profile) and not discover):
        raise ValueError("invalid workspace selection")
    if not box_id:
        if profile != "mina":
            raise ValueError("explicit Box required")
        return execute_selected(native, home, request, alive, profile)
    if not BOX.fullmatch(box_id):
        raise ValueError("invalid Box identity")
    from native_profiles import locked
    with locked(home):
        if discover:
            selected = profiles(home, box_id)
            if len(selected) != 1:
                raise Conflict()
            profile = selected[0][0].name
        directory, box = resolve(home, box_id, profile, boxes_file)
        return execute_selected(native, directory, request, alive, profile, box)


def execute_selected(native, home, request, alive, profile, box=None):
    operation = request["operation"]
    with profile_environment(home):
        from hermes_time import get_timezone_name
        zone = get_timezone_name() or "Main local time"
        selected = dict(timezone=zone, profile=profile, box_id=box["box_id"] if box else "")
        def project(job, detail=False):
            return projection(job, detail, profile, box)
        if operation == "workspace_info":
            name = "MINA" if profile == "mina" else profile.replace("_", " ").replace("-", " ").title()
            return dict(selected, profiles=[dict(id=profile, name=name, timezone=zone, box_id=selected["box_id"])],
                        items=[project(j) for j in strict_jobs(home)])
        if operation == "workspace_detail":
            job = next((j for j in strict_jobs(home) if j["id"] == request.get("id")), None)
            if not job:
                raise Conflict()
            return dict(selected, job=project(job, True))
        token = request.get("token", "")
        if not isinstance(token, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{16,128}", token):
            raise ValueError("invalid token")
        with locked_native_store(native, home):
            initial = strict_jobs(home)
            signature = digest({k: v for k, v in request.items() if k != "token" and (k != "box_id" or v)})
            receipt_key = "creation_token" if operation == "workspace_create" else "token"
            replay = next((j for j in initial if (j.get("origin") or {}).get(MARKER, {}).get(receipt_key) == token), None)
            if replay:
                receipt = replay["origin"][MARKER]
                if receipt.get("creation_signature" if operation == "workspace_create" else "signature") != signature:
                    raise Conflict()
                return dict(selected, job=project(replay, True))
            job = next((j for j in initial if j["id"] == request.get("id")), None)
            if operation != "workspace_create":
                if not job or revision(job, box) != request.get("expected_revision") or project_managed(job) or terminal(job):
                    raise Conflict()
                project(job)
            spec = request.get("spec")
            if operation in ("workspace_create", "workspace_edit"):
                if not isinstance(spec, dict) or set(spec) != {"name", "prompt", "schedule"}:
                    raise ValueError("invalid spec")
                for key, limit in (("name", 160), ("prompt", 32768), ("schedule", 256)):
                    if not isinstance(spec[key], str) or not spec[key].strip() or len(spec[key].encode()) > limit:
                        raise ValueError("invalid spec")
                parsed = native.parse_schedule(spec["schedule"])
                if operation == "workspace_edit" and (not paused(job) or not project(job, True)["editable"]):
                    raise Conflict()
            with transaction(native, initial) as current:
                if operation == "workspace_create":
                    origin = {BOX_CONTEXT: {k: box[k] for k in CONTEXT_FIELDS}} if box else {}
                    job = native.create_job(prompt=spec["prompt"], schedule=spec["schedule"], name=spec["name"],
                                            paused=True, paused_reason="Created in LOOM Workspace; explicit resume required", deliver="local",
                                            workdir=box["workdir"] if box else str(Path(home).parent), origin=origin)
                elif operation == "workspace_edit":
                    updates=dict(name=spec["name"], prompt=spec["prompt"], schedule=parsed)
                    old_kind=(job.get("schedule") or {}).get("kind")
                    if parsed.get("kind") == "once" and old_kind != "once":
                        completed=(job.get("repeat") or {}).get("completed", 0)
                        if not isinstance(completed,int) or completed<0:
                            raise Conflict()
                        updates["repeat"]=dict(times=completed+1,completed=completed)
                    elif old_kind == "once" and parsed.get("kind") != "once":
                        updates["repeat"]=dict(times=None,completed=(job.get("repeat") or {}).get("completed",0))
                    job = native.update_job(job["id"], updates)
                elif operation == "workspace_pause":
                    job = native.pause_job(job["id"], reason="Paused in LOOM Workspace")
                elif operation == "workspace_resume":
                    if not paused(job):
                        raise Conflict()
                    # Explicit resume starts at a future occurrence, never a
                    # retained missed slot. The native parser still owns timing.
                    native.update_job(job["id"], {"next_run_at": None})
                    job = native.resume_job(job["id"])
                    next_run=datetime.fromisoformat(str(job.get("next_run_at") or "").replace("Z","+00:00"))
                    if next_run.tzinfo is None or next_run<=native._hermes_now():
                        raise Conflict()
                if not job:
                    raise Conflict()
                origin = copy.deepcopy(job.get("origin") or {})
                receipt=copy.deepcopy(origin.get(MARKER) or {})
                receipt.update(token=token, signature=signature)
                if operation == "workspace_create":
                    receipt.update(creation_token=token, creation_signature=signature)
                origin[MARKER] = receipt
                job = native.update_job(job["id"], {"origin": origin})
                saved = current()
            commit(native, saved, alive)
            return dict(selected, job=project(job, True))
