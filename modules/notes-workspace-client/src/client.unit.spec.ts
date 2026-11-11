import { describe, it, expect, vi } from "vitest";
import { Journal, recoveryPersistence, type State, type Persistence } from "./journal";
import { IntentClient, type ClientIO } from "./client";
import { digest, validate, type Binding } from "./protocol";
import { readContent, readAsBlob } from "@vrtmrz/livesync-commonlib/compat/common/utils";
const base: Binding = {
    version: 1,
    kind: "binding",
    id: "b",
    workspace: "w",
    collection: "c",
    generation: "g",
    fileId: "f",
    collectionRoot: "Notes",
    path: "Notes/a.md",
    sourceBase: "A",
    nativeRevision: "1-a",
    sha256: "",
    writable: true,
};
describe("client compact status API", () => {
    it("keeps an explicitly reviewed native retirement exact to its revision and local absence",async()=>{
        const f=await fixture(),path="Notes/reviewed.md";
        await f.j.update(s=>{(s.pathChanges??={})["reviewed-native-retirement/"+JSON.stringify([path,"1-old"])]=true;});
        await f.c.beforeReflect({path,_rev:"1-old"} as any);
        expect(f.j.state.holds[path]).toBe("observed_absence/reviewed_native_retirement");
        await f.c.beforeReflect({path,_rev:"2-new"} as any);
        expect(f.j.state.holds[path]).toBe("reflection_waiting_for_binding_or_intent");
        f.files.set(path,"new local edit");
        await f.c.beforeReflect({path,_rev:"1-old"} as any);
        expect(f.j.state.holds[path]).toBe("reflection_waiting_for_binding_or_intent");
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("classifies absent unbound native tombstones and scanner deletes without a manual recheck",async()=>{
        const f=await fixture(),path="Notes/cancelled.md";
        await f.c.beforeReflect({path,_rev:"2-deleted",deleted:true} as any);
        expect(f.j.state.holds[path]).toBe("observed_absence/unbound_native_retirement");
        await f.c.mutation({kind:"delete",info:path as any});
        expect(f.j.state.holds[path]).toBe("observed_absence/uncaptured_delete");
        expect(f.c.summary().scanObservations).toBe(1);
        expect(f.c.summary().holds-f.c.summary().scanObservations).toBe(0);
        expect(f.j.state.operations).toHaveLength(0);
        await f.c.beforeReflect({path,_rev:"3-new"} as any);
        expect(f.j.state.holds[path]).toBe("reflection_waiting_for_binding_or_intent");
        f.files.set(path,"new local bytes");
        await f.c.beforeReflect({path,_rev:"4-deleted",deleted:true} as any);
        expect(f.j.state.holds[path]).toBe("reflection_waiting_for_binding_or_intent");
    });
    it("keeps deletion warnings for source bindings and unfinished requests",async()=>{
        const f=await fixture();
        f.files.delete(f.b.path);
        await f.c.beforeReflect({path:f.b.path,_rev:"2-deleted",deleted:true} as any);
        expect(f.j.state.holds[f.b.path]).toBe("reflection_waiting_for_binding_or_intent");
        const path="Notes/pending.md";f.files.set(path,"pending");
        f.c.capture({kind:"create",path});await f.c.settled();f.files.delete(path);
        await f.c.beforeReflect({path,_rev:"2-deleted",deleted:true} as any);
        expect(f.j.state.holds[path]).toBe("reflection_waiting_for_binding_or_intent");
        expect(f.j.state.operations.some(o=>o.intent.path===path&&!o.resolvedBy)).toBe(true);
    });
    it("treats existing read-only view containers as structure, not failed edits",async()=>{
        const f=await fixture();const path="Projects/_Archived";f.io.directoryExists=async p=>p===path;
        await f.c.ingest({version:1,kind:"scope",id:"archived",workspace:"w",collection:"archive",generation:"a",collectionRoot:path+"/Old/Notes",writable:false,sourceSequence:1});
        await f.c.hold(path,"directory_scope_unavailable");await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[path]).toBe("source_managed_structure");
        expect(f.c.summary().scanObservations).toBe(1);expect(f.j.state.operations).toHaveLength(0);
    });
    it("accepts exact foreign deletion receipts without recreating stale warnings",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};f.files.delete(f.b.path);
        const i={version:1 as const,kind:"intent" as const,id:"foreign_delete",device:"other",operation:"delete" as const,
            workspace:"w",collection:"c",generation:"g",fileId:"f",base:f.b,path:f.b.path,sha256:await digest(""),length:0};
        const sha=await digest(JSON.stringify(i));
        const p={version:1 as const,kind:"publication" as const,id:"foreign_publication",intentId:i.id,intentDigest:sha,
            revisions:[{path:i.path,revision:"2-deleted",role:"deletion" as const}]};
        await f.j.update(s=>{s.intents={[i.id]:i};s.publications={[p.id]:p};s.acks={ack:{version:1,kind:"ack",id:"ack",intentId:i.id,
            publicationId:p.id,intentDigest:sha,status:"applied",reason:"source_applied"}};s.holds[i.path]="reflection_waiting_for_binding_or_intent";});
        expect(await f.c.beforeReflect({path:i.path,_rev:"2-deleted",deleted:true} as any)).toBe(true);
        f.io.retiredRevision=async()=>false;
        await f.c.reconcileSafetyHolds();expect(f.j.state.holds[i.path]).toBeDefined();
        f.io.retiredRevision=async(_p,rev)=>rev==="2-deleted";
        await f.c.reconcileSafetyHolds();expect(f.j.state.holds[i.path]).toBeUndefined();
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("captures another A-to-B transfer after a completed A-B-A cycle",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"library",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",writable:true,sourceSequence:1});
        f.files.delete(f.b.path);f.files.set("Library/a.md","A");
        const first=f.c.capture({kind:"rename",oldPath:f.b.path,path:"Library/a.md"});await f.c.settled();
        await f.j.update(s=>{s.operations[0].state="applied";s.operations[0].transferResolvedBy="old_cleanup";});
        await f.c.install({...f.b,id:"retired_library",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",path:"Library/a.md",deleted:true});
        await f.c.install({...f.b,id:"returned",fileId:"returned_file",sourceSequence:5});
        const second=f.c.capture({kind:"rename",oldPath:f.b.path,path:"Library/a.md"});await f.c.settled();
        expect(second).toBeDefined();expect(second).not.toBe(first);
        expect(f.j.state.operations).toHaveLength(2);
        expect(f.j.state.operations[1].transfer?.source.binding.fileId).toBe("returned_file");
    });
    it("keeps an applied layout's old controls retired and verifies its destination before clearing warnings",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        const old={...f.b,sourceSequence:1},next={...old,id:"layout_next",collectionRoot:"Library",path:"Library/a.md",sourceSequence:2,nativeRevision:"2-next"};
        f.files.delete(old.path);f.files.set(next.path,"A");f.revisions.set(next.nativeRevision,"A");
        f.io.conflictLeaves=async()=>[{revision:next.nativeRevision,text:"A"}];
        f.io.retiredRevision=async(path,rev)=>path===old.path&&rev===old.nativeRevision;
        await f.j.update(s=>{delete s.bindings[old.path];s.bindings[next.path]=next;s.candidates[old.id]=old;s.candidates[next.id]=next;
            s.layouts={layout:{control:{version:1,kind:"layout",id:"layout",previous:old,binding:next},state:"applied"}};});
        expect(await f.c.beforeReflect({path:old.path,_rev:old.nativeRevision} as any)).toBe(true);
        expect(f.j.state.holds[old.path]).toBeUndefined();
        await f.c.hold(old.path,"reflection_waiting_for_binding_or_intent");await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[old.path]).toBeUndefined();
        f.files.set(next.path,"new local edit");await f.c.hold(old.path,"reflection_waiting_for_binding_or_intent");
        await f.c.reconcileSafetyHolds();expect(f.j.state.holds[old.path]).toBeDefined();
    });
    it("keeps explicit device exclusions and absent unbound paths as qualified observations",async()=>{
        const f=await fixture();f.io.referenceAllowed=async()=>false;
        await f.c.hold("Notes/excluded.pdf","reflection_chunks_pending");
        await f.c.hold("Notes/gone.canvas","missing_writable_binding");
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds["Notes/excluded.pdf"]).toBe("device_excluded/reflection_chunks_pending");
        expect(f.j.state.holds["Notes/gone.canvas"]).toBe("observed_absence/missing_writable_binding");
        expect(f.c.summary().scanObservations).toBe(2);
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("qualifies obsolete move history without claiming native deletion or dismissing changed destination bytes",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        const next={...f.b,id:"moved",collection:"topics",generation:"fresh",collectionRoot:"Topics",path:"Topics/a.md",sourceSequence:1};
        f.files.delete(f.b.path);f.files.set(next.path,"A");f.io.conflictLeaves=async()=>[{revision:next.nativeRevision,text:"A"}];
        f.io.retiredRevision=async()=>false;
        await f.j.update(s=>{delete s.bindings[f.b.path];s.bindings[next.path]=next;
            s.candidates={old:{...f.b,id:"legacy",fileId:"legacy_file",generation:"legacy"},[f.b.id]:f.b};
            s.layouts={move:{state:"applied",control:{version:1,kind:"layout",id:"move",sourceMove:true,previous:f.b,binding:next}}};});
        await f.c.ingest({version:1,kind:"scope",id:"source_scope",workspace:"w",collection:"c",generation:"g",collectionRoot:"Notes",writable:true,sourceSequence:2});
        await f.c.ingest({version:1,kind:"scope",id:"target_scope",workspace:"w",collection:"topics",generation:"fresh",collectionRoot:"Topics",writable:true,sourceSequence:2});
        await f.c.hold(f.b.path,"uncaptured_delete");await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[f.b.path]).toBe("observed_absence/accepted_source_move");
        await f.c.mutation({kind:"delete",info:f.b.path as any});
        expect(f.j.state.holds[f.b.path]).toBe("observed_absence/accepted_source_move");
        f.files.set(next.path,"new local bytes");await f.c.hold(f.b.path,"uncaptured_delete");await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[f.b.path]).toBe("uncaptured_delete");
    });
    it("does not resurrect reference warnings after a source move completes during a read",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        const old:Binding={...f.b,id:"reference",path:"Notes/old.pdf",writable:false};
        const next:Binding={...old,id:"moved_reference",path:"Library/new.pdf",collectionRoot:"Library",sourceSequence:2};
        await f.j.update(s=>{s.bindings[old.path]=old;s.candidates[old.id]=old;});
        f.io.readReference=async()=>{
            await f.j.update(s=>{delete s.bindings[old.path];s.bindings[next.path]=next;
                s.layouts={move:{state:"applied",control:{version:1,kind:"layout",id:"move",sourceMove:true,previous:old,binding:next}}};});
            return null;
        };
        await f.c.mutation({kind:"store",info:old.path as any});
        expect(f.j.state.holds[old.path]).toBeUndefined();
        expect(f.j.state.bindings[next.path]).toEqual(next);
        await f.c.beforeReflect({path:old.path,_rev:"2-late",deleted:true} as any);
        expect(f.j.state.holds[old.path]).toBeUndefined();
        f.files.set(old.path,"new unregistered local content");
        await f.c.beforeReflect({path:old.path,_rev:"3-late",deleted:true} as any);
        expect(f.j.state.holds[old.path]).toBeDefined();
    });
    it("retains real reference changes at the current binding",async()=>{
        const f=await fixture();
        const b:Binding={...f.b,id:"reference",path:"Notes/current.pdf",writable:false};
        await f.j.update(s=>{s.bindings[b.path]=b;});
        f.io.readReference=async()=>new Uint8Array([1,2,3]);
        await f.c.mutation({kind:"store",info:b.path as any});
        expect(f.j.state.holds[b.path]).toBe("reference_local_change");
    });
    it("retains an exact reviewed external-move disposition without suppressing a new source identity",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        await f.c.ingest(f.b);
        await f.j.update(s=>{(s.pathChanges??={})["reviewed-source-move/"+f.b.id]=true;delete s.bindings[f.b.path];});
        f.files.delete(f.b.path);
        expect(await f.c.beforeReflect({path:f.b.path,_rev:f.b.nativeRevision} as any)).toBe(true);
        await f.c.reflectCandidate(f.b);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        expect(f.files.has(f.b.path)).toBe(false);
        f.c.capture({kind:"delete",path:f.b.path});await f.c.settled();
        await f.c.mutation({kind:"delete",info:f.b.path as any});
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        expect(f.j.state.operations).toHaveLength(0);
        const next={...f.b,id:"new-source",nativeRevision:"2-new",sha256:await digest("new source")};
        await f.c.ingest(next);f.revisions.set(next.nativeRevision,"new source");
        await f.c.reflectCandidate(next);
        expect(f.j.state.pathChanges?.["reviewed-source-move/"+next.id]).toBeUndefined();
    });
    it("retires a cross-collection source only after destination acceptance, including after restart",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"library",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",writable:true,sourceSequence:1});
        f.files.delete(f.b.path);f.files.set("Library/a.md","A");
        const id=f.c.capture({kind:"rename",oldPath:f.b.path,path:"Library/a.md"});await f.c.settled();
        expect(f.j.state.operations).toHaveLength(1);
        expect(f.j.state.operations[0]).toMatchObject({intent:{id,operation:"create",collection:"lib"},transfer:{source:{path:f.b.path}}});
        await f.c.mutation({kind:"rename",oldPath:f.b.path as any,info:"Library/a.md" as any});
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        await f.c.resumeTransfers();expect(f.j.state.operations).toHaveLength(1);
        await f.j.update(s=>{const o=s.operations[0];o.state="conflict";o.ack={version:1,kind:"ack",id:"refusal",intentId:id!,intentDigest:"sha256:"+"0".repeat(64),publicationId:"pub",status:"conflict",reason:"occupied"};});
        await f.c.resumeTransfers();expect(f.j.state.operations).toHaveLength(1);
        await f.j.update(s=>{const o=s.operations[0];o.state="applied";o.ack={...o.ack!,status:"applied",reason:"source_applied"};});
        const c=new IntentClient(f.j,f.io);await c.ready;c.onReflect=()=>{};
        await c.resumeTransfers();expect(f.j.state.operations).toHaveLength(2);
        expect(f.j.state.operations[1]).toMatchObject({intent:{operation:"delete",path:f.b.path,base:f.b},transferDeleteOf:id});
        await c.resumeTransfers();expect(f.j.state.operations).toHaveLength(2);
        expect(f.files.get("Library/a.md")).toBe("A");
    });
    it("does not retire a source path recreated during a cross-collection transfer",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"library",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",writable:true,sourceSequence:1});
        f.files.delete(f.b.path);f.files.set("Library/a.md","A");
        const id=f.c.capture({kind:"rename",oldPath:f.b.path,path:"Library/a.md"});await f.c.settled();
        await f.j.update(s=>{const o=s.operations[0];o.state="applied";o.ack={version:1,kind:"ack",id:"ack",intentId:id!,intentDigest:"sha256:"+"0".repeat(64),publicationId:"pub",status:"applied",reason:"source_applied"};});
        f.files.set(f.b.path,"recreated source");await f.c.resumeTransfers();
        expect(f.j.state.operations).toHaveLength(1);expect(f.j.state.holds[f.b.path]).toBe("transfer_source_recreated");
    });
    it("keeps folder source cleanup behind child transfer receipts and handles duplicate callbacks",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        const old="Notes/Folder",path=old+"/a.md",directory={...f.b,id:"dir_old",fileId:"directory",path:old,entryKind:"directory" as const,sha256:await digest("")};
        await f.j.update(s=>{delete s.bindings[f.b.path];s.bindings[old]=directory;s.bindings[path]={...f.b,path};});
        const c=new IntentClient(f.j,f.io);await c.ready;c.onReflect=()=>{};
        await c.ingest({version:1,kind:"scope",id:"library",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",writable:true,sourceSequence:1});
        await c.ingest({...directory,id:"previous-deleted-target",fileId:"previous-target",workspace:"w",collection:"lib",generation:"lib",collectionRoot:"Library",path:"Library/Folder",deleted:true,sourceSequence:9});
        f.files.set("Library/Folder/a.md","A");
        const id=c.capture({kind:"rename",oldPath:old,path:"Library/Folder",folder:true});await c.settled();
        c.capture({kind:"rename",oldPath:path,path:"Library/Folder/a.md"});await c.settled();
        expect(f.j.state.operations).toHaveLength(2);
        const child=f.j.state.operations.find(o=>o.intent.path.endsWith("a.md"))!;
        expect(f.j.state.operations.find(o=>o.intent.id===id)?.transfer?.children).toEqual([child.intent.id]);
        const echo={...directory,id:"new-directory-echo",sourceSequence:10};
        f.io.reflectDirectory=vi.fn();await c.ingest(echo);await c.reflectCandidate(echo);
        expect(f.io.reflectDirectory).not.toHaveBeenCalled();
        await f.j.update(s=>{for(const o of s.operations){o.state="applied";o.ack={version:1,kind:"ack",id:"ack_"+o.intent.id,intentId:o.intent.id,intentDigest:"sha256:"+"0".repeat(64),publicationId:"pub_"+o.intent.id,status:"applied",reason:"source_applied"};}});
        await c.resumeTransfers();expect(f.j.state.operations).toHaveLength(3);
        const removed=f.j.state.operations[2];expect(removed.intent.path).toBe(path);
        await f.j.update(s=>{const o=s.operations[2];o.state="applied";o.ack={version:1,kind:"ack",id:"child_delete",intentId:o.intent.id,intentDigest:"sha256:"+"0".repeat(64),publicationId:"pub",status:"applied",reason:"source_applied"};});
        await c.resumeTransfers();expect(f.j.state.operations[3]).toMatchObject({intent:{path:old,operation:"delete",dependencies:[removed.intent.id]},transferDeleteOf:id});
    });
    it("selects the newest binding when several configurations share an exact native revision",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        f.io.conflictLeaves=async()=>[{revision:f.b.nativeRevision,text:"A"}];
        await f.j.update(s=>{s.bindings[f.b.path]={...f.b,sourceSequence:1};});
        for(let seq=2;seq<=4;seq++)await f.c.ingest({...f.b,id:"g"+seq,generation:"g"+seq,sourceBase:"base"+seq,sourceSequence:seq});
        await f.c.ingest({version:1,kind:"scope",id:"scope_current",workspace:"w",collection:"c",generation:"g4",collectionRoot:"Notes",writable:true,sourceSequence:4});
        await f.c.hold(f.b.path,"reflection_waiting_for_binding_or_intent");
        const result=await f.c.reconcileSafetyHolds();
        expect(result.cleared).toBe(1);
        expect(f.j.state.bindings[f.b.path].id).toBe("g4");
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        await f.c.beforeReflect({path:f.b.path,_rev:f.b.nativeRevision} as any);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
    });
    it("adopts a re-keyed current-generation Canvas only when local bytes and the sole native leaf are unchanged",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        const old={...f.b,path:"Notes/map.canvas"},next={...old,id:"fresh_canvas",fileId:"fresh_source_id",generation:"fresh",sourceBase:"fresh_base"};
        f.files.set(old.path,"A");f.io.conflictLeaves=async()=>[{revision:old.nativeRevision,text:"A"}];
        await f.j.update(s=>{s.bindings[old.path]=old;s.candidates[old.id]=old;s.candidates[next.id]=next;});
        await f.c.ingest({version:1,kind:"scope",id:"fresh_scope",workspace:"w",collection:"c",generation:"fresh",collectionRoot:"Notes",sourceSequence:2,writable:true});
        await f.c.hold(old.path,"binding_base_collision");
        f.files.set(old.path,"unaccepted local edit");
        await f.c.reconcileSafetyHolds();expect(f.j.state.bindings[old.path]).toEqual(old);
        f.files.set(old.path,"A");
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.bindings[old.path]).toEqual(next);
        expect(f.j.state.holds[old.path]).toBeUndefined();
        expect(f.j.state.candidates[old.id]).toEqual(old);
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("does not replay old-generation controls or mistake retained history for a new delete",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"scope_current",workspace:"w",collection:"c",generation:"new",collectionRoot:"Notes",writable:true,sourceSequence:2});
        f.files.delete(f.b.path);
        await f.c.hold(f.b.path,"uncaptured_delete");
        await f.c.reflectCandidate(f.b);
        expect(await f.c.beforeReflect({path:f.b.path,_rev:f.b.nativeRevision} as any)).toBe(true);
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[f.b.path]).toBe("configuration_retained_history");
        expect(f.c.summary().scanObservations).toBe(1);
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.files.has(f.b.path)).toBe(false);
        f.files.set(f.b.path,"unregistered local change");
        await f.c.hold(f.b.path,"reflection_preserves_local_edit");
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[f.b.path]).toBe("reflection_preserves_local_edit");
    });
    it("submits a new exact-current-base edit for an unchanged-source configuration handover", async () => {
        const f=await fixture(); f.c.onReflect=()=>{};
        const old={...f.b,sourceSequence:1};
        await f.j.update(s=>{s.bindings[old.path]=old;});
        const c=new IntentClient(f.j,f.io); await c.ready; c.onReflect=()=>{};
        f.files.set(old.path,"saved offline"); c.capture({kind:"edit",path:old.path});await c.settled();
        const first=f.j.state.operations[0];
        const fresh={...old,id:"current",generation:"current",sourceSequence:2,nativeRevision:"2-source",sourceBase:"currentbase"};
        f.revisions.set(fresh.nativeRevision,"A");
        f.io.conflictLeaves=async()=>[{revision:fresh.nativeRevision,text:"A"}];
        await c.ingest({version:1,kind:"scope",id:"scope_current",workspace:"w",collection:"c",generation:"current",collectionRoot:"Notes",writable:true,sourceSequence:2});
        await c.ingest(fresh); await c.reflectCandidate(fresh); await c.settled();
        expect(f.j.state.operations).toHaveLength(2);
        expect(f.j.state.operations[0]).toEqual(first);
        expect(f.j.state.operations[1]).toMatchObject({payload:"saved offline",state:"queued",intent:{generation:"current",base:fresh},resolution:{covered:[first.intent.id]}});
        expect(f.j.state.operations[1].intent.predecessor).toBeUndefined();
        await c.reflectCandidate(fresh); expect(f.j.state.operations).toHaveLength(2);
    });
    it("preserves changed sources and destructive operations instead of auto-handing them over", async()=>{
        for(const kind of ["edit","delete"] as const){
            const f=await fixture();f.c.onReflect=()=>{};
            f.files.set(f.b.path,"saved local");f.c.capture({kind,path:f.b.path});await f.c.settled();
            const fresh={...f.b,id:"current",generation:"current",sourceSequence:2,nativeRevision:"2-source",sha256:await digest("changed source")};
            f.revisions.set(fresh.nativeRevision,"changed source"); f.io.conflictLeaves=async()=>[{revision:fresh.nativeRevision,text:"changed source"}];
            await f.c.ingest({version:1,kind:"scope",id:"scope_current",workspace:"w",collection:"c",generation:"current",collectionRoot:"Notes",writable:true,sourceSequence:2});
            await f.c.ingest(fresh); await f.c.reflectCandidate(fresh);
            expect(f.j.state.operations).toHaveLength(1);
            expect(f.files.get(f.b.path)).toBe("saved local");
        }
    });
    it("clears warnings only after exact current materialization and retains unresolved local edits", async()=>{
        const f=await fixture(); f.io.conflictLeaves=async()=>[{revision:f.b.nativeRevision,text:"A"}];
        await f.c.hold(f.b.path,"reflection_waiting_for_binding_or_intent");
        await f.c.hold("Notes/unknown.md","untracked_scan");
        expect(await f.c.reconcileSafetyHold(f.b.path)).toBe(true);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        f.files.set(f.b.path,"unsaved elsewhere");await f.c.hold(f.b.path,"reflection_preserves_local_edit");
        const page=await f.c.reconcileSafetyHolds();
        expect(page.cleared).toBe(0);expect(Object.keys(f.j.state.holds)).toHaveLength(2);
    });
    it("never calls full diagnostic formatting for summary or paged status", async () => {
        const f = await fixture();
        const report = vi.spyOn(f.c, "details").mockImplementation(() => { throw Error("full_report_not_for_status"); });
        const first = f.c.summary();
        expect(f.c.summary()).toBe(first);
        expect(first).toMatchObject({ phase: "ready", unfinished: 0, holds: 0 });
        await f.c.hold("Notes/no-operation.md", "missing_writable_binding");
        expect(f.c.summary()).toMatchObject({ holds: 1, captureHolds: 1 });
        const overview = f.c.statusDetails();
        expect(overview.groups.total).toBe(1);
        expect(f.c.statusItem(overview.groups.items[0].id)!.hold?.reason).toBe("missing_writable_binding");
        expect(report).not.toHaveBeenCalled();
    });
    it("exposes retained full operations separately from the bounded overview", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "retained edit");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        const row = f.c.statusDetails().groups.items[0];
        expect(f.c.summary()).toMatchObject({ unfinished: 1, pendingPublication: 1 });
        expect(f.c.statusOperation(row.operationId!)!.payload).toBe("retained edit");
        expect(f.c.statusItem(row.id)!.operations.total).toBe(1);
        expect(f.c.details()).toContain("pending source acceptance");
    });
});
describe("client recovery export", () => {
    it("recognizes delayed callbacks only for confirmed current directory retirements", async () => {
        const f = await fixture(), path = "Notes/Retired";
        f.c.onReflect = () => {};
        f.io.directoryExists = async () => false;
        f.io.retireEmptyDirectory = async () => false;
        await f.c.ingest({ version: 1, kind: "scope", id: "scope", workspace: "w", collection: "c",
            generation: "g", collectionRoot: "Notes", writable: true, sourceSequence: 1 });
        const old: Binding = { ...f.b, id: "directory_old", fileId: "directory", path,
            entryKind: "directory", sha256: await digest(""), sourceSequence: 1 };
        const gone = { ...old, id: "directory_gone", deleted: true, sourceSequence: 2 };
        await f.c.install(old); await f.c.ingest(gone); await f.c.reflectCandidate(gone);
        f.c.capture({ kind: "delete", folder: true, path }); await f.c.settled();
        expect(f.j.state.holds[path]).toBeUndefined();
        expect(f.j.state.operations).toHaveLength(0);
        await f.c.hold(path, "directory_base_unavailable");
        // Older clients retained the source control but no extra retirement marker.
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[path]).toBeUndefined();
        const recreated = { ...old, id: "directory_recreated", fileId: "other", sourceSequence: 3 };
        await f.c.ingest(recreated);
        await f.c.hold(path, "directory_base_unavailable"); await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[path]).toBe("directory_base_unavailable");
        await f.j.update(s => { delete s.candidates[recreated.id]; });
        f.io.directoryExists = async p => p === path;
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[path]).toBe("directory_base_unavailable");
        f.io.directoryExists = async () => false;
        await f.j.update(s => { s.scopes!["w/c"].generation = "new"; });
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.holds[path]).toBe("directory_base_unavailable");
    });
    it("retains child-first delete dependencies after child cursors have gone",async()=>{
        const f=await fixture();
        await f.j.update(s=>{
            for(const [name,id] of [["Folder","parent"],["Folder/Empty","child"]]){
                const path="Notes/"+name;
                s.bindings[path]={...f.b,id:"dir_"+id,fileId:id,path,entryKind:"directory",sha256:"sha256:"+"0".repeat(64)};
            }
        });
        const c=new IntentClient(f.j,f.io);await c.ready;
        c.capture({kind:"delete",folder:true,path:"Notes/Folder/Empty"});
        c.capture({kind:"delete",folder:true,path:"Notes/Folder"});
        c.capture({kind:"delete",folder:true,path:"Notes/Folder/Empty"});
        await c.settled();
        const ops=f.j.state.operations;
        expect(ops).toHaveLength(2);
        expect(ops[1].intent.dependencies).toEqual([ops[0].intent.id]);
        expect(f.j.state.holds["Notes/Folder/Empty"]).toBeUndefined();
    });
    it("freezes directory dependencies to only prior operations in that subtree", async () => {
        const f=await fixture();
        await f.j.update(s=>{
            for(const part of ["tree","tree/Left","tree/Right"]){
                const path="Notes/"+part;
                s.bindings[path]={...f.b,id:"dir_"+part.replace(/\//g,"_"),fileId:part.replace(/\//g,"_"),path,
                    entryKind:"directory",sha256:"sha256:"+"0".repeat(64)};
            }
            for(const part of ["Left","Right"]){
                const path=`Notes/tree/${part}/a.md`;
                s.bindings[path]={...f.b,id:"b"+part,fileId:"f"+part,path};
                f.files.set(`Notes/moved/${part}/a.md`,"A");
            }
        });
        const c=new IntentClient(f.j,f.io);await c.ready;
        c.capture({kind:"rename",folder:true,oldPath:"Notes/tree",path:"Notes/moved"});await c.settled();
        c.capture({kind:"rename",folder:true,oldPath:"Notes/tree/Left",path:"Notes/moved/Left"});await c.settled();
        c.capture({kind:"rename",oldPath:"Notes/tree/Left/a.md",path:"Notes/moved/Left/a.md"});await c.settled();
        const ops=f.j.state.operations;
        expect(ops).toHaveLength(5);
        expect(f.j.state.holds["Notes/tree/Left"]).toBeUndefined();
        expect(f.j.state.holds["Notes/tree/Left/a.md"]).toBeUndefined();
        for(const op of ops){
            expect(validate(op.intent)).toEqual(op.intent);
            for(const id of op.intent.dependencies??[]){
                const child=ops.find(o=>o.intent.id===id)!;
                expect(child.intent.path.startsWith(op.intent.path+"/")).toBe(true);
            }
        }
        expect(ops.find(o=>o.intent.path==="Notes/tree/Left")?.intent.dependencies).toHaveLength(1);
        expect(ops.find(o=>o.intent.path==="Notes/tree/Right")?.intent.dependencies).toHaveLength(1);
        expect(ops.find(o=>o.intent.path==="Notes/tree")?.intent.dependencies).toHaveLength(4);
    });
    it("does not replay an older directory tombstone over a recreated path",async()=>{
        const f=await fixture();
        const old:Binding={...f.b,id:"dir_old",fileId:"oldDir",path:"Notes/Recreated",entryKind:"directory",deleted:true,sourceSequence:8,sha256:await digest("")};
        const fresh:Binding={...old,id:"dir_new",fileId:"newDir",deleted:false,sourceSequence:9};
        await f.j.update(s=>{s.candidates[old.id]=old;s.candidates[fresh.id]=fresh});
        f.io.reflectDirectory=vi.fn(async()=>{});
        await f.c.reflectCandidate(old);
        expect(f.io.reflectDirectory).not.toHaveBeenCalled();
        await f.c.reflectCandidate(fresh);
        expect(f.io.reflectDirectory).toHaveBeenCalledWith(fresh);
        await f.c.reflectCandidate(old);
        expect(f.io.reflectDirectory).toHaveBeenCalledTimes(1);
    });
    it.each(["canvas", "base", "empty"])("wakes only the retiring parent after its %s child settles", async kind => {
        const f = await fixture(), parent = "Notes/Old";
        f.c.onReflect = vi.fn();
        const directory: Binding = { ...f.b, id: "parent", fileId: "parent", path: parent, entryKind: "directory", sha256: await digest(""), sourceSequence: 1 };
        const child: Binding = { ...f.b, id: "child", fileId: "child", path: parent + (kind === "empty" ? "/Empty" : "/board." + kind),
            ...(kind === "empty" ? { entryKind: "directory" as const } : {}), sha256: await digest("") };
        const tombstone = { ...directory, id: "parent_deleted", deleted: true, sourceSequence: 2 };
        const unrelated = { ...tombstone, id: "unrelated", fileId: "unrelated", path: "Notes/Other" };
        await f.c.install(directory); await f.c.install(child);
        await f.c.ingest(tombstone); await f.c.ingest(unrelated);
        const dirs = new Set([parent]);
        f.io.directoryExists = async p => dirs.has(p);
        f.io.retireEmptyDirectory = vi.fn(async b => dirs.delete(b.path));
        await f.c.reflectPath(parent);
        expect(f.io.retireEmptyDirectory).not.toHaveBeenCalled();
        expect(f.j.state.holds[parent]).toBe("directory_waiting_for_children");
        await f.c.reconcileSafetyHolds();
        expect(f.j.state.bindings[parent]).toEqual(directory);
        expect(dirs.has(parent)).toBe(true);
        vi.mocked(f.c.onReflect).mockClear();
        await f.c.install({ ...child, id: "child_deleted", deleted: true, sourceSequence: 3 });
        expect(f.c.onReflect).toHaveBeenCalledWith(parent);
        expect(f.c.onReflect).not.toHaveBeenCalledWith(unrelated.path);
        await f.c.reflectPath(parent);
        expect(dirs.has(parent)).toBe(false);
        expect(f.j.state.bindings[parent]).toBeUndefined();
        expect(f.j.state.holds[parent]).toBeUndefined();
        expect(f.j.state.candidates[tombstone.id]).toEqual(tombstone);
    });
    it("keeps a tombstone held for pending child targets and unmanaged local children", async () => {
        const f = await fixture(), parent = "Notes/Old";
        f.c.onReflect = vi.fn();
        const directory: Binding = { ...f.b, id: "parent", fileId: "parent", path: parent, entryKind: "directory", sha256: await digest(""), sourceSequence: 1 };
        const tombstone = { ...directory, id: "parent_deleted", deleted: true, sourceSequence: 2 };
        await f.c.install(directory); await f.c.ingest(tombstone);
        f.files.set(parent + "/draft.md", "draft"); f.files.delete(f.b.path);
        f.c.capture({ kind: "rename", oldPath: f.b.path, path: parent + "/draft.md" }); await f.c.settled();
        f.io.directoryExists = async p => p === parent;
        f.io.retireEmptyDirectory = vi.fn(async () => false);
        await f.c.reflectPath(parent);
        expect(f.j.state.holds[parent]).toBe("directory_pending_intent");
        expect(f.io.retireEmptyDirectory).not.toHaveBeenCalled();
        await f.j.update(s => { s.operations[0].state = "applied"; });
        await f.c.reflectPath(parent + "/draft.md");
        await f.c.reflectPath(parent);
        expect(f.j.state.holds[parent]).toBe("directory_preserves_local_children");
        expect(f.j.state.bindings[parent]).toEqual(directory);
        expect(f.j.state.operations[0].payload).toBe("draft");
    });
    it("retries a rename from a local child event without recursively moving unknown children", async () => {
        const f = await fixture(), from = "Notes/Old", to = "Notes/New";
        f.c.onReflect = vi.fn();
        const a: Binding = { ...f.b, id: "parent", fileId: "parent", path: from, entryKind: "directory", sha256: await digest(""), sourceSequence: 1 };
        const b: Binding = { ...a, id: "renamed", path: to, previousPath: from, sourceSequence: 2 };
        const dirs = new Set([from]);
        let localChild = true;
        f.io.directoryExists = async path => dirs.has(path);
        f.io.reflectDirectory = vi.fn(async binding => { expect(binding.previousPath).toBeUndefined(); dirs.add(binding.path); });
        f.io.retireEmptyDirectory = vi.fn(async binding => !localChild && dirs.delete(binding.path));
        await f.c.install(a); await f.c.ingest(b); await f.c.reflectPath(to);
        expect(f.j.state.holds[to]).toBe("directory_preserves_local_children");
        expect(f.j.state.bindings[from]).toEqual(a);
        expect(f.j.state.reflecting[to]).toBeUndefined();
        localChild = false; vi.mocked(f.c.onReflect).mockClear();
        f.c.capture({ kind: "delete", path: from + "/unmanaged.txt" }); await f.c.settled();
        expect(f.c.onReflect).toHaveBeenCalledWith(to);
        await f.c.reflectPath(to);
        expect(dirs.has(from)).toBe(false); expect(dirs.has(to)).toBe(true);
        expect(f.j.state.bindings[from]).toBeUndefined(); expect(f.j.state.bindings[to]).toEqual(b);
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("rechecks pending children after ensuring a renamed directory target", async () => {
        const f = await fixture(), from = "Notes/Old";
        f.c.onReflect = vi.fn();
        const a: Binding = { ...f.b, id: "parent", fileId: "parent", path: from, entryKind: "directory", sha256: await digest(""), sourceSequence: 1 };
        const b: Binding = { ...a, id: "renamed", path: "Notes/New", previousPath: from, sourceSequence: 2 };
        await f.c.install(a); await f.c.ingest(b);
        f.io.directoryExists = async path => path === from;
        f.io.retireEmptyDirectory = vi.fn(async () => true);
        f.io.reflectDirectory = async () => {
            f.files.set(from + "/draft.canvas", '{"nodes":[],"edges":[]}');
            f.c.capture({ kind: "create", path: from + "/draft.canvas" });
        };
        await f.c.reflectPath(b.path);
        expect(f.io.retireEmptyDirectory).not.toHaveBeenCalled();
        expect(f.j.state.holds[b.path]).toBe("directory_pending_intent");
        expect(f.j.state.bindings[from]).toEqual(a);
        expect(f.j.state.operations[0].payload).toBe('{"nodes":[],"edges":[]}');
    });
    it("does not treat an untracked local folder as a managed tombstone predecessor", async () => {
        const f = await fixture(); f.c.onReflect = vi.fn();
        const b: Binding = { ...f.b, id: "deleted_dir", fileId: "other_dir", path: "Notes/Untracked", entryKind: "directory", deleted: true, sha256: await digest(""), sourceSequence: 3 };
        f.io.directoryExists = async p => p === b.path;
        f.io.retireEmptyDirectory = vi.fn(async () => true);
        await f.c.ingest(b); await f.c.reflectPath(b.path);
        expect(f.io.retireEmptyDirectory).not.toHaveBeenCalled();
        expect(f.j.state.holds[b.path]).toBe("directory_previous_unavailable");
        expect(f.j.state.reflecting[b.path]).toBeUndefined();
    });
    it("captures an empty directory before any note or file binding exists", async () => {
        const j = new Journal({ load:async()=>undefined, save:async()=>{} });
        const c = new IntentClient(j,{read:async()=>null,readControl:async()=>{throw Error("unused")},
            readRevision:async()=>null,descends:async()=>false,reflect:async()=>{},changed:()=>{}});
        await c.ready;
        await c.ingest({version:1,kind:"scope",id:"scope",workspace:"w",collection:"c",generation:"g",
            collectionRoot:"Topics",sourceSequence:1,writable:true});
        c.capture({kind:"create",folder:true,path:"Topics/Empty folder"});
        await c.settled();
        expect(j.state.operations).toHaveLength(1);
        const op=j.state.operations[0];
        expect(op.intent.entryKind).toBe("directory");
        expect(op.intent.base).toBeNull();
        expect(op.intent.path).toBe("Topics/Empty folder");
        expect(op.payload).toBe("");
        expect(validate(op.intent)).toEqual(op.intent);
        c.capture({kind:"rename",folder:true,path:"Topics/Renamed",oldPath:"Topics/Empty folder"});
        await c.settled();
        expect(j.state.operations[1].intent.predecessor).toBe(op.intent.id);
        expect(j.state.operations[1].intent.fileId).toBe(op.intent.fileId);
    });
    it.each(["canvas", "base"])("uses collection scope for a byte-exact %s create", async extension => {
        const f=await fixture();
        const path=`Notes/folder/board.${extension}`,text=extension === "canvas" ? '{"nodes":[],"edges":[]}\r\n' : 'views:\r\n  - type: table\r\n    name: Caf\u00e9\r\n';
        f.files.set(path,text);
        f.c.capture({kind:"create",path});await f.c.settled();
        expect(f.j.state.operations[0].payload).toBe(text);
        expect(f.j.state.operations[0].intent.sha256).toBe(await digest(text));
        expect(validate(f.j.state.operations[0].intent).kind).toBe("intent");
    });
    it("honours device reference exclusions before payload reads and preserves durable state", async () => {
        const f = await fixture();
        const b = { ...f.b, path: "Notes/book.pdf", writable: false };
        await f.j.update(s => {
            s.bindings[b.path] = b;
            s.candidates[b.id] = b;
            s.reflecting[b.path] = b;
        });
        f.io.referenceAllowed = vi.fn(async () => false);
        f.io.readReference = vi.fn(async () => { throw Error("must_not_load_excluded_reference"); });
        const recovered = new IntentClient(f.j, f.io);
        await recovered.ready;
        expect(await recovered.mutation({ kind: "store", info: b.path as never })).toBe(true);
        const entry = { path: b.path, _rev: b.nativeRevision } as any;
        expect(await recovered.beforeReflect(entry)).toBe(true);
        await recovered.afterReflect(entry, true);
        expect(f.io.readReference).not.toHaveBeenCalled();
        expect(f.j.state.bindings[b.path]).toEqual(b);
        expect(f.j.state.reflecting[b.path]).toEqual(b);
        expect(f.j.state.holds[b.path]).toBeUndefined();
        // Markdown still follows the normal exact-base reflection path.
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        expect(await recovered.beforeReflect({ path: f.b.path, _rev: f.b.nativeRevision } as any)).toBeUndefined();
        expect(f.io.referenceAllowed).not.toHaveBeenCalledWith(f.b.path, f.b.nativeRevision);
    });
    it.each(["canvas", "svg", "csv"])("decodes explicit binary %s metadata before extension fallback", async extension => {
        const expected = new TextEncoder().encode("Unicode \u03bb and independent chunks");
        const data = [expected.slice(0, 5), expected.slice(5)].map(bytes => btoa(String.fromCharCode(...bytes)));
        const entry = { path: `Notes/reference.${extension}`, type: "newnote", datatype: "newnote", data } as any;
        expect(new Uint8Array(readContent(entry) as ArrayBuffer)).toEqual(expected);
        expect(new Uint8Array(await readAsBlob(entry).arrayBuffer())).toEqual(expected);
        expect(await digest(new Uint8Array(readContent(entry) as ArrayBuffer))).toBe(await digest(expected));
        expect(readContent({ ...entry, type: "plain", datatype: "plain", data: ["plain text"] })).toBe("plain text");
        expect(readContent({ path: entry.path, data: ["legacy text"] } as any)).toBe("legacy text");
    });
    it("does not park a revision waiting for chunks as a permanent hash mismatch", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "late", nativeRevision: "2-b", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.io.reflect = vi.fn(async () => { await f.c.beforeReflect({ path: b.path, _rev: b.nativeRevision } as any); });
        await f.c.reflectPath(b.path);
        expect(f.j.state.holds[b.path]).toBe("reflection_chunks_pending");
        f.revisions.set(b.nativeRevision, "B");
        await f.c.reflectPath(b.path);
        expect(f.j.state.reflecting[b.path]).toEqual(b);
        expect(f.io.reflect).toHaveBeenCalledTimes(2);
    });
    it("parks a stable bad revision and retries only its path when explicitly requested", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "bad", nativeRevision: "2-b", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.io.reflect = vi.fn(async () => { await f.c.hold(b.path, "reflection_hash_mismatch"); });
        await f.c.reflectPath(b.path);
        await f.c.reflectPath(b.path);
        expect(f.io.reflect).toHaveBeenCalledTimes(1);
        await f.c.reflectPath("Notes/unrelated.md");
        expect(f.io.reflect).toHaveBeenCalledTimes(1);
        await f.c.reflectPath(b.path, true);
        expect(f.io.reflect).toHaveBeenCalledTimes(2);
        const next = { ...b, id: "repaired", nativeRevision: "3-c" };
        await f.j.update(s => { s.candidates[next.id] = next; });
        await f.c.reflectPath(b.path);
        expect(f.io.reflect).toHaveBeenLastCalledWith(next);
        expect(Object.keys(f.j.state.candidates)).toHaveLength(2);
    });
    it("checks no-op guards against serialized state before cloning", async () => {
        const save = vi.fn(async (_s: State) => {});
        const j = new Journal({ load: async () => undefined, save });
        await j.ready;
        await j.update(s => { s.holds.x = "pending"; });
        const clone = vi.spyOn(globalThis, "structuredClone");
        try {
            await j.update(() => { throw Error("must_not_run"); }, s => s.holds.x === "pending");
            expect(clone).not.toHaveBeenCalled();
            const first = j.update(s => { delete s.holds.x; });
            const second = j.update(s => { s.holds.x = "pending"; }, s => s.holds.x === "pending");
            await Promise.all([first, second]);
            expect(j.state.holds.x).toBe("pending");
            expect(save).toHaveBeenCalledTimes(4);
        } finally { clone.mockRestore(); }
    });
    it("does not rewrite durable recovery exports for unchanged state", async () => {
        const save = vi.fn(async (_s: State) => {});
        const j = new Journal({ load: async () => undefined, save });
        await j.ready;
        await j.update(() => {});
        expect(save).toHaveBeenCalledTimes(1);
        await j.update(s => { s.holds.x = "pending"; });
        await j.update(s => { s.holds.x = "pending"; });
        expect(save).toHaveBeenCalledTimes(2);
    });
    it("alternates full checkpoints only after durable save and preserves pending bytes", async () => {
        const slots = new Map<number, string>();
        let saved: State | undefined;
        const persistence = recoveryPersistence({ load: async () => saved, save: async s => { saved = s; } }, "vault-1",
            async (slot, content) => {
                expect(JSON.parse(content).state).toEqual(saved);
                slots.set(slot, content);
            });
        const j = new Journal(persistence);
        await j.ready;
        await j.checkpoint();
        await j.update(s => { s.holds["note.md"] = "preserve_local_variant"; });
        expect(slots.size).toBe(1);
        await j.checkpoint();
        expect(slots.size).toBe(2);
        expect(JSON.parse(slots.get(1)!).state.holds["note.md"]).toBe("preserve_local_variant");
        const previous = slots.get(1);
        await j.update(s => { s.holds["second.md"] = "pending"; });
        await j.checkpoint();
        expect(slots.get(1)).toBe(previous);
        expect(JSON.parse(slots.get(0)!).vaultId).toBe("vault-1");
    });
    it("does not treat failed durable storage as a backed-up save", async () => {
        const write = vi.fn();
        const persistence = recoveryPersistence({ load: async () => undefined, save: async () => { throw Error("disk"); } }, "vault", write);
        const j = new Journal(persistence);
        await expect(j.ready).rejects.toThrow("disk");
        expect(write).not.toHaveBeenCalled();
    });
});
export async function fixture() {
    const b = { ...base, sha256: await digest("A") };
    let saved: State | undefined;
    const persistence: Persistence = {
        load: async () => structuredClone(saved),
        save: async (s) => {
            saved = structuredClone(s);
        },
    };
    const j = new Journal(persistence);
    await j.ready;
    await j.update((s) => {
        s.bindings[b.path] = b;
    });
    const files = new Map([[b.path, "A"]]);
    const revisions = new Map([[b.nativeRevision, "A"]]);
    const io: ClientIO = {
        read: async (p) => files.get(p) ?? null,
        readRevision: async (_p, r) => revisions.get(r) ?? null,
        readControl: vi.fn(),
        descends: async (_p, r, a) => Number(r.split("-")[0]) > Number(a.split("-")[0]),
        reflect: async () => {},
        changed: vi.fn(),
    };
    const c = new IntentClient(j, io);
    await c.ready;
    return { b, j, c, io, files, revisions, persistence };
}
describe("durable admission", () => {
    it("does not capture implicit folders created while reflecting a source file", async()=>{
        const f=await fixture();
        await f.j.update(s=>{s.reflecting["Notes/new/child/note.md"]={...f.b,path:"Notes/new/child/note.md"};});
        f.c.capture({kind:"create",folder:true,path:"Notes/new"});
        f.c.capture({kind:"create",folder:true,path:"Notes/new/child"});
        await f.c.settled();expect(f.j.state.operations).toHaveLength(0);
        f.c.capture({kind:"create",folder:true,path:"Notes/unrelated"});await f.c.settled();
        expect(f.j.state.operations).toHaveLength(1);
    });
    it.each(["directory_identity_collision","configuration_changed"])("retains a refused directory ensure satisfied by a current source binding: %s",async reason=>{
        const f=await fixture(),p="Notes/source-folder";f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"scope",workspace:"w",collection:"c",generation:"g",collectionRoot:"Notes",sourceSequence:1,writable:true});
        f.c.capture({kind:"create",folder:true,path:p});await f.c.settled();
        const before=structuredClone(f.j.state.operations[0]);
        await f.j.update(s=>{s.operations[0].state="held";s.operations[0].reason=reason;});
        const b:Binding={...f.b,id:"dir_source",fileId:"source_directory",path:p,entryKind:"directory",sha256:await digest(""),sourceSequence:4};
        f.io.directoryExists=async path=>path===p;f.io.reflectDirectory=async next=>f.c.install(next);
        await f.c.ingest(b);await f.c.reflectCandidate(b);
        const op=f.j.state.operations[0];
        expect(op.intent).toEqual(before.intent);expect(op.state).toBe("held");expect(op.sourceSatisfiedBy).toEqual(b);
        expect(f.c.summary().unfinished).toBe(0);expect(f.c.summary().accepted).toBe(0);expect(f.c.summary().resolved).toBe(1);
        expect(f.j.state.bindings[p]).toEqual(b);
    });
    it.each(["missing","readonly","delete","dependent","stale"])("does not dismiss an unproven directory request: %s",async scenario=>{
        const f=await fixture(),p="Notes/source-folder";f.c.onReflect=()=>{};
        await f.c.ingest({version:1,kind:"scope",id:"scope",workspace:"w",collection:"c",generation:"g",collectionRoot:"Notes",sourceSequence:1,writable:true});
        f.c.capture({kind:"create",folder:true,path:p});await f.c.settled();
        await f.j.update(s=>{
            s.operations[0].state="held";s.operations[0].reason="directory_identity_collision";
            if(scenario==="delete")s.operations[0].intent.operation="delete";
            if(scenario==="dependent")s.operations.push({...s.operations[0],intent:{...s.operations[0].intent,id:"dependent",path:p+"/child",predecessor:s.operations[0].intent.id}});
        });
        const b:Binding={...f.b,id:"dir_source",fileId:"source_directory",path:p,entryKind:"directory",sha256:await digest(""),sourceSequence:4,writable:scenario!=="readonly"};
        f.io.directoryExists=async()=>scenario!=="missing";
        if(scenario==="stale")await f.c.ingest({...b,id:"dir_newer",sourceSequence:5,deleted:true});
        await f.c.ingest(b);await f.c.reflectCandidate(b);
        expect(f.j.state.operations[0].sourceSatisfiedBy).toBeUndefined();
    });
    it("adopts a known source parent instead of creating a competing directory identity",async()=>{
        const f=await fixture();f.c.onReflect=()=>{};const path="Notes/Folder";
        const b={...f.b,id:"directory",fileId:"directory",path,entryKind:"directory" as const,sha256:await digest("")};
        await f.c.ingest(b);f.io.directoryExists=async p=>p===path;f.io.reflectDirectory=async next=>{await f.c.install(next);};
        f.files.set(path+"/new.md","new");await f.c.registerLocalNote(path+"/new.md");
        expect(f.j.state.operations).toHaveLength(1);expect(f.j.state.operations[0].intent.path).toBe(path+"/new.md");
        expect(f.j.state.bindings[path].id).toBe(b.id);
    });
    it("registers a preserved local note through a proven enrollment transition", async () => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 1 };
        const current = { ...old, id: "current", generation: "current", sourceSequence: 2 };
        await f.j.update(s => {
            s.bindings[old.path] = current;
            s.bindings["Notes/retained.md"] = { ...old, path: "Notes/retained.md", fileId: "retained" };
            s.candidates[old.id] = old;
            s.candidates[current.id] = current;
        });
        f.files.set("Notes/local.md", "preserved local bytes");
        await f.c.registerLocalNote("Notes/local.md");
        const op = f.j.state.operations[0];
        expect(op.intent.operation).toBe("create");
        expect(op.intent.generation).toBe("current");
        expect(op.intent.base).toBeNull();
        expect(op.payload).toBe("preserved local bytes");
        await expect(f.c.registerLocalNote("Notes/local.md")).rejects.toThrow("already_tracked");
        f.files.set("Notes/new.md", "new bytes");
        f.c.capture({ kind: "create", path: "Notes/new.md" });
        await f.c.settled();
        expect(f.j.state.operations[1].intent.generation).toBe("current");
    });
    it.each(["unproven", "branch", "cycle", "other_scope"])("holds %s enrollment ambiguity", async kind => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 1 };
        const next = { ...old, id: "next", generation: "next", sourceSequence: 2 };
        await f.j.update(s => {
            s.bindings[old.path] = old;
            s.bindings["Notes/other.md"] = { ...next, path: "Notes/other.md", fileId: "other" };
            s.candidates[old.id] = old;
            if (kind !== "unproven") s.candidates[next.id] = next;
            if (kind === "branch") s.candidates.branch = { ...next, id: "branch", generation: "branch" };
            if (kind === "cycle") s.candidates.cycle = { ...old, id: "cycle", sourceSequence: 3 };
            if (kind === "other_scope") s.bindings.scope = { ...old, workspace: "another" };
        });
        f.files.set("Notes/local.md", "do not lose");
        await expect(f.c.registerLocalNote("Notes/local.md")).rejects.toThrow("scope_ambiguous");
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.files.get("Notes/local.md")).toBe("do not lose");
    });
    it("admits one create when local registration is requested concurrently", async () => {
        const f = await fixture();
        f.files.set("Notes/local.md", "local bytes");
        const results = await Promise.allSettled([
            f.c.registerLocalNote("Notes/local.md"), f.c.registerLocalNote("Notes/local.md"),
        ]);
        expect(results.filter(r => r.status === "fulfilled")).toHaveLength(1);
        expect(f.j.state.operations).toHaveLength(1);
    });
    it("skips installed candidates without traversing the retained candidate corpus", async () => {
        const f = await fixture();
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        const values = vi.spyOn(Object, "values");
        f.io.reflect = vi.fn();
        try {
            await f.c.reflectCandidate(f.b);
            expect(values).not.toHaveBeenCalled();
            expect(f.io.reflect).not.toHaveBeenCalled();
        } finally { values.mockRestore(); }
        await f.c.hold(f.b.path, "native_conflict_requires_resolution");
        await f.c.reflectCandidate(f.b);
        expect(f.io.reflect).toHaveBeenCalledWith(f.b);
    });
    it("invalidates path lookup after durable candidate changes and reports reflection holds", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "second", nativeRevision: "2-b", sourceBase: "B", sha256: await digest("B") };
        await f.j.update(s => { s.candidates[f.b.id] = f.b; });
        await f.c.beforeReflect({ path: f.b.path, _rev: f.b.nativeRevision } as any);
        await f.j.update(s => { s.candidates[b.id] = b; });
        f.revisions.set(b.nativeRevision, "B");
        expect(await f.c.beforeReflect({ path: b.path, _rev: b.nativeRevision } as any)).toBeUndefined();
        await f.c.hold("Notes/attachment.canvas", "reflection_hash_mismatch");
        expect(f.c.details().split("\n")[0]).toContain("1 reflection holds");
    });
    it("adopts an advanced enrollment without replaying withdrawn bindings", async () => {
        const f = await fixture();
        const old = { ...f.b, sourceSequence: 12 };
        const next = { ...old, id: "expanded", generation: "expanded", sourceSequence: 13,
            nativeRevision: "2-expanded", sourceBase: "expandedbase" };
        await f.j.update(s => { s.bindings[old.path] = old; s.candidates[next.id] = next; });
        f.revisions.set(next.nativeRevision, "A");
        const entry = { path: next.path, _rev: next.nativeRevision } as any;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[next.path]).toEqual(next);
        f.io.reflect = vi.fn();
        await f.c.reflectCandidate(old);
        expect(f.io.reflect).not.toHaveBeenCalled();
        await f.c.stopNative();
    });
    it("coalesces only accepted descendant display requests before installation", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        const older = { ...f.b, id: "older", nativeRevision: "2-b" };
        const newer = { ...f.b, id: "newer", nativeRevision: "3-c" };
        await f.j.update(s => { s.candidates[older.id] = older; s.candidates[newer.id] = newer; });
        await f.c.reflectCandidate(older);
        expect(f.io.reflect).not.toHaveBeenCalled();
        await f.c.reflectCandidate(newer);
        expect(f.io.reflect).toHaveBeenCalledWith(newer);
        expect(Object.keys(f.j.state.candidates)).toHaveLength(2);
        f.io.descends = async () => false;
        await f.c.reflectCandidate(older);
        expect(f.io.reflect).toHaveBeenLastCalledWith(older);
    });
    it("reflects exact binary references, preserves local changes and never publishes edits", async () => {
        const f = await fixture();
        const bytes = new Uint8Array([137, 80, 78, 71, 0, 255]);
        const b: Binding = { ...f.b, id: "reference", fileId: "ref", path: "Notes/image.png", writable: false, sha256: await digest(bytes) };
        expect(validate(b)).toEqual(b);
        expect(() => validate({ ...b, writable: true })).toThrow();
        let local: Uint8Array<ArrayBuffer> | null = null;
        f.io.readReference = async (_path, rev) => rev ? bytes : local;
        await f.j.update(s => { s.candidates[b.id] = b; });
        const entry = { path: b.path, _rev: b.nativeRevision } as any;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        local = bytes;
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[b.path]).toEqual(b);
        const next = { ...b, id: "nextref", sourceBase: "nextbase", nativeRevision: "2-next" };
        await f.j.update(s => { s.candidates[next.id] = next; });
        local = new Uint8Array([1, 2, 3]);
        expect(await f.c.beforeReflect({ ...entry, _rev: next.nativeRevision })).toBe(true);
        expect(f.j.state.holds[b.path]).toBe("reflection_preserves_local_edit");
        f.c.capture({ kind: "edit", path: b.path });
        await f.c.settled();
        await f.c.mutation({ kind: "store", info: b.path as any });
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[b.path]).toBe("reference_local_change");
    });
    it("does not reflect while a local edit or conflict is unresolved", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.files.set(f.b.path, "offline");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        const candidate = { ...f.b, id: "incoming", nativeRevision: "2-server" };
        await f.c.reflectCandidate(candidate);
        await f.j.update(s => { s.operations[0].state = "conflict"; });
        await f.c.reflectCandidate(candidate);
        expect(f.io.reflect).not.toHaveBeenCalled();
    });

    it.each(["rename", "delete"] as const)("skips retired %s history without hiding newer or unrelated controls", async kind => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.files.delete(f.b.path);
        const target = "Notes/renamed.md";
        if (kind === "rename") f.files.set(target, "A");
        f.c.capture({ kind, path: kind === "rename" ? target : f.b.path,
            ...(kind === "rename" ? { oldPath: f.b.path } : {}) });
        await f.c.settled();
        await f.j.update(s => {
            s.operations[0].state = "applied";
            delete s.bindings[f.b.path];
        });
        f.io.descends = vi.fn(async (_p, r, a) => r === "1-a" && a === "0-old");
        await f.c.reflectCandidate(f.b);
        await f.c.reflectCandidate({ ...f.b, id: "older", nativeRevision: "0-old" });
        expect(f.io.reflect).not.toHaveBeenCalled();
        for (const change of [{ nativeRevision: "2-new" }, { generation: "other" }, { fileId: "other" }]) {
            const candidate = { ...f.b, id: crypto.randomUUID(), ...change };
            await f.c.reflectCandidate(candidate);
            expect(f.io.reflect).toHaveBeenLastCalledWith(candidate);
        }
    });

    it("does not ask native reflection to materialize installed or proven ancestor controls", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        await f.c.reflectCandidate(f.b);
        const next = { ...f.b, id: "next", nativeRevision: "2-next" };
        await f.c.install(next);
        f.io.descends = vi.fn(async (_p, revision, ancestor) =>
            revision === "2-next" && ancestor === "1-a");
        await f.c.reflectCandidate(f.b);
        expect(f.io.reflect).not.toHaveBeenCalled();
        expect(f.io.descends).toHaveBeenCalledWith(f.b.path, "2-next", "1-a");
    });

    it("keeps newer, unrelated, different-scope and same-revision collision candidates visible", async () => {
        const f = await fixture();
        f.io.reflect = vi.fn();
        f.io.descends = vi.fn(async () => false);
        for (const change of [
            { nativeRevision: "2-new" },
            { nativeRevision: "1-other" },
            { sourceBase: "different" },
            { generation: "other", nativeRevision: "0-old" },
            { fileId: "other", nativeRevision: "0-old" },
        ]) {
            const candidate = { ...f.b, id: crypto.randomUUID(), ...change };
            await f.c.reflectCandidate(candidate);
            expect(f.io.reflect).toHaveBeenLastCalledWith(candidate);
        }
        expect(f.io.reflect).toHaveBeenCalledTimes(5);
    });

    it("clears supersession when the latest stable save equals the retained base", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "A");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
        f.files.set(f.b.path, "later");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations[0].intent.base).toEqual(f.b);
        expect(f.j.state.operations[0].intent.predecessor).toBeUndefined();
    });

    it.each(["rename", "delete"] as const)("does not bypass an ambiguous %s with an edit", async (kind) => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        const target = "Notes/b.md";
        f.files.delete(f.b.path);
        if (kind === "rename") f.files.set(target, "newer");
        f.c.capture({
            kind,
            path: kind === "rename" ? target : f.b.path,
            ...(kind === "rename" ? { oldPath: f.b.path } : {}),
        });
        await f.c.settled();
        f.files.set(kind === "rename" ? target : f.b.path, "later");
        f.c.capture({ kind: "edit", path: kind === "rename" ? target : f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
    });
    it("keeps a missing-payload capture held instead of treating it as supersession", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "later");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        expect(f.j.state.holds[f.b.path]).toBe("predecessor_capture_held");
    });

    it("recovers overlapping ordinary saves and a later settled save without restarting", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "superseded");
        f.c.capture({ kind: "edit", path: f.b.path });
        f.files.set(f.b.path, "latest");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["latest"]);
        f.files.set(f.b.path, "third");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["latest", "third"]);
        expect(f.j.state.operations[0].intent.base).toEqual(f.b);
        expect(f.j.state.operations[0].intent.predecessor).toBeUndefined();
        expect(f.j.state.operations[1].intent.predecessor).toBe(f.j.state.operations[0].intent.id);
        expect(f.j.state.holds[f.b.path]).toBeUndefined();
    });

    it("queues an offline reversion to the old base after a pending edit", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "B");
        f.c.capture({ kind: "edit", path: f.b.path });
        await f.c.settled();
        f.files.set(f.b.path, "A");
        await f.c.mutation({ kind: "store", info: f.b.path as never });
        expect(f.j.state.operations.map((o) => o.payload)).toEqual(["B", "A"]);
        expect(f.j.state.operations[1].intent.predecessor).toBe(f.j.state.operations[0].intent.id);
    });

    it("retains materialized A for stale delete and chains rename followed by edit", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.files.set("Notes/b.md", "A");
        f.c.capture({ kind: "rename", oldPath: f.b.path, path: "Notes/b.md" });
        await f.c.settled();
        f.files.set("Notes/b.md", "B");
        f.c.capture({ kind: "edit", path: "Notes/b.md" });
        await f.c.settled();
        f.files.delete("Notes/b.md");
        f.c.capture({ kind: "delete", path: "Notes/b.md" });
        await f.c.settled();
        const ops = f.j.state.operations;
        expect(ops.map((o) => o.intent.operation)).toEqual(["rename", "edit", "delete"]);
        expect(ops[1].intent.predecessor).toBe(ops[0].intent.id);
        expect(ops[2].intent.predecessor).toBe(ops[1].intent.id);
        expect(ops[2].intent.base?.sourceBase).toBe("A");
        const restart = new Journal(f.persistence);
        await restart.ready;
        expect(restart.state.operations).toEqual(ops);
        expect(ops[1].payload).toBe("B");
    });
    it("captures offline edits, but never creates deletes from scanner absence or manual resolution", async () => {
        const f = await fixture();
        f.files.set(f.b.path, "offline");
        await f.c.mutation({ kind: "store", info: f.b.path as never });
        expect(f.j.state.operations[0].payload).toBe("offline");
        f.files.delete(f.b.path);
        await f.c.mutation({ kind: "delete", info: f.b.path as never });
        expect(await f.c.mutation({ kind: "resolve", info: f.b.path as never })).toBe(false);
        expect(f.j.state.operations).toHaveLength(1);
        expect(f.j.state.holds[f.b.path]).toBe("native_conflict_requires_resolution");
    });
    it("installs exact reflected revision without generating user intent and preserves concurrent local edit", async () => {
        const f = await fixture();
        const b = { ...f.b, id: "new", sourceBase: "B", nativeRevision: "2-b", sha256: await digest("B") };
        f.revisions.set("2-b", "B");
        await f.c.ingest(b);
        const entry = { path: b.path, _rev: b.nativeRevision } as never;
        expect(await f.c.beforeReflect(entry)).toBeUndefined();
        f.files.set(b.path, "B");
        f.c.capture({ kind: "edit", path: b.path });
        await f.c.settled();
        await f.c.afterReflect(entry, true);
        expect(f.j.state.bindings[b.path].sourceBase).toBe("B");
        expect(f.j.state.operations).toHaveLength(0);
        const next = { ...b, id: "next", sourceBase: "C", nativeRevision: "3-c", sha256: await digest("C") };
        f.revisions.set("3-c", "C");
        await f.c.ingest(next);
        f.files.set(b.path, "local");
        expect(await f.c.beforeReflect({ path: b.path, _rev: "3-c" } as never)).toBe(true);
        expect(f.j.state.operations[0].payload).toBe("local");
    });
    it("holds unknown files, cross-collection moves and journal failure without losing prior bytes", async () => {
        const f = await fixture();
        f.c.capture({ kind: "edit", path: "Unknown.md" });
        await f.c.settled();
        expect(f.j.state.operations).toHaveLength(0);
        f.files.set("Outside/b.md", "A");
        f.c.capture({ kind: "rename", oldPath: f.b.path, path: "Outside/b.md" });
        await f.c.settled();
        expect(f.j.state.holds[f.b.path]).toBe("outside_collection");
        const g = await fixture();
        g.files.set(g.b.path, "saved");
        g.c.capture({ kind: "edit", path: g.b.path });
        await g.c.settled();
        g.persistence.save = async () => {
            throw new Error("quota");
        };
        g.files.set(g.b.path, "new");
        g.c.capture({ kind: "edit", path: g.b.path });
        await g.c.settled();
        expect(g.j.failure).toContain("quota");
        expect(g.j.state.operations[0].payload).toBe("saved");
        expect(g.j.state.operations).toHaveLength(1);
    });
    it("expands a folder rename using known identities and holds the batch after a capture gap", async () => {
        const f = await fixture();
        f.files.delete(f.b.path);
        f.files.set("Notes/sub/a.md", "A");
        f.c.capture({ kind: "rename", oldPath: "Notes", path: "Notes/sub", folder: true });
        await f.c.settled();
        expect(f.j.state.operations[0].intent.target).toBe("Notes/sub/a.md");
        expect(f.j.state.operations[0].intent.fileId).toBe("f");
    });
    it.each(["rename", "delete"] as const)("captures every known descendant of a large folder %s", async kind => {
        const f = await fixture();
        await f.j.update(s => {
            for (let k = 0; k < 257; k++) {
                const path = `Notes/folder/${k}.md`;
                s.bindings[path] = { ...f.b, id: `b${k}`, fileId: `f${k}`, path };
                if (kind === "rename") f.files.set(`Notes/moved/${k}.md`, "A");
            }
        });
        const c = new IntentClient(f.j, f.io);
        await c.ready;
        c.capture({ kind, folder: true, path: kind === "rename" ? "Notes/moved" : "Notes/folder",
            ...(kind === "rename" ? { oldPath: "Notes/folder" } : {}) });
        await c.settled();
        expect(f.j.state.operations).toHaveLength(257);
        expect(new Set(f.j.state.operations.map(o => o.intent.fileId)).size).toBe(257);
        expect(f.j.state.operations.every(o => o.intent.operation === kind)).toBe(true);
        expect(f.j.state.bindings[f.b.path]).toEqual(f.b);
        expect(f.j.state.holds["Notes/folder"]).toBe("directory_base_unavailable");
    });
});
