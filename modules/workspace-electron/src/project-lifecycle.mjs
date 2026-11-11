import {randomUUID} from 'node:crypto';
import {savedRoot} from './box-roots.mjs';

const within=(value,parent)=>value===parent||value.startsWith(parent+'/');
const ref=value=>{if(typeof value!=='string'||!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(value)||value.includes('..'))throw Error('Invalid project reference.');return value;};
export class ProjectLifecycle {
  constructor(notes,remote,{checkDrafts=()=>{},checkUploads=()=>{}}={}) {
    Object.assign(this,{notes,remote,checkDrafts,checkUploads});
    notes.db.exec('CREATE TABLE IF NOT EXISTS project_lifecycle (id TEXT PRIMARY KEY, record TEXT NOT NULL)');
  }
  get(id){const row=this.notes.db.prepare('SELECT record FROM project_lifecycle WHERE id=?').get(ref(id));if(!row)throw Error('Saved project review not found.');return JSON.parse(row.record);}
  save(op){this.notes.db.prepare('INSERT INTO project_lifecycle VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record').run(op.id,JSON.stringify(op));}
  list(){return this.notes.db.prepare('SELECT record FROM project_lifecycle ORDER BY rowid DESC LIMIT 100').all().map(row=>JSON.parse(row.record));}
  blocked(relative,root='box'){
    const pathOperation=this.paths?.blocked(relative,root);if(pathOperation)return pathOperation;
    for(const row of this.notes.db.prepare('SELECT record FROM project_lifecycle').iterate()) {
      const op=JSON.parse(row.record);
      if(savedRoot(op)===root&&!['preview','complete','rejected'].includes(op.state)&&within(relative,op.boxPath))return op;
    }
  }
  checkAvailable(relative,root='box'){if(this.blocked(relative,root))throw Error('A project lifecycle operation needs review. Your local note is preserved.');}
  async preview(project,kind,{root='box',box_id}={}){
    if(!['archive','restore'].includes(kind))throw Error('Choose Archive or Restore.');
    const binding=this.notes.boxes.identity(root,box_id);
    const {data:plan}=await this.remote.api(`/v1/projects/${ref(project)}/archive/${kind==='restore'?'restore/':''}plan`,binding,{method:'POST',body:{reason:'Reviewed in LOOM Workspace.'}});
    if(plan.project_id!==project||plan.workspace?.operation_kind!==kind||!plan.plan_digest||!plan.workspace.operation_id)throw Error('Main did not return the exact project plan.');
    const box=kind==='archive'?plan.workspace.source:plan.workspace.destination;
    const internalRoot=box.root==='box'&&binding.box_id&&plan.workspace.box_id===binding.box_id;
    if((box.root!==root&&!internalRoot)||!box.relative_path||box.relative_path.split('/').includes('..'))throw Error('Main returned an invalid Box path.');
    const op={id:`project_${randomUUID().replaceAll('-','')}`,...binding,project,kind,boxPath:box.relative_path,plan,state:'preview',at:new Date().toISOString()};this.save(op);return op;
  }
  async checkLocal(op,{recover=false}={}){
    const binding=this.notes.boxes.identity(savedRoot(op),op.box_id);
    if(!recover)await this.checkDrafts(op.boxPath,binding);await this.checkUploads(op.boxPath,binding);
    for(const r of this.notes.byID.values()) {
      if(r.deleted||r.root!==binding.root||!within(r.path,op.boxPath))continue;
      if(r.pending||r.held||r.pathOperation||r.treeOperation||r.externalText!==undefined||r.localAbsence)throw Error(`Resolve pending local changes in ${r.path} before ${op.kind}. No files were moved.`);
      if(!recover&&!r.directory&&(await this.notes.local.read(r.localPath)).text!==r.text)throw Error(`An external local edit in ${r.path} is preserved. Save it before ${op.kind}.`);
    }
  }
  async apply(id,{recover=false}={}){
    return this.notes.change(async()=>{
      const op=this.get(id);if(op.state==='complete')return op;
      const binding=this.notes.boxes.identity(savedRoot(op),op.box_id);
      if(op.state==='rejected')throw Error('This review was rejected before mutation. Create a fresh review.');
      if(!recover&&op.state!=='preview')throw Error('Inspect this saved operation and explicitly recover it; do not submit another plan.');
      if(recover&&op.state==='preview')throw Error('This review has not been submitted.');
      await this.checkLocal(op,{recover});
      op.state='submitted';delete op.error;this.save(op);this.notes.emit('project-lifecycle',{boxPath:op.boxPath,state:op.state,operation:op.kind,project:op.project});
      const plan=op.plan;
      try{
        const body=recover?{operation_id:plan.workspace.operation_id,plan_digest:plan.plan_digest,confirm:true}:{plan,plan_digest:plan.plan_digest,confirm:true};
        const {data}=await this.remote.api(`/v1/projects/${ref(op.project)}/archive/${op.kind==='restore'?'restore/':''}${recover?'recover':'apply'}`,binding,{method:'POST',body});
        if(data.operation_id!==plan.workspace.operation_id||data.project_id!==op.project||data.plan_digest!==plan.plan_digest)throw Error('The source receipt does not match the saved project operation.');
        op.receipt=data;
        const complete=data.phase==='complete' && (op.kind!=='restore'||data.activation_state==='inactive');
        // Phase names are owned by Main. A blocked inactive restore is normal,
        // but an unknown phase is never promoted to completion.
        op.state=complete?'complete':'needs_review';
      }catch(error){op.state=!recover&&error.code==='project_archive.review_stale'&&!error.receipt?'rejected':'needs_review';op.error=error.message;if(error.receipt?.operation_id===plan.workspace.operation_id)op.receipt=error.receipt;}
      if(op.state==='complete'&&op.kind==='archive')for(const r of this.notes.byID.values())if(!r.deleted&&r.root===binding.root&&within(r.path,op.boxPath)){r.retainedProject=op.id;this.notes.persist(r);}
      if(op.state==='complete'&&op.kind==='restore')for(const r of this.notes.byID.values())if(!r.deleted&&r.retainedProject&&r.root===binding.root&&within(r.path,op.boxPath)){r.restoreReady=op.id;this.notes.persist(r);}
      this.save(op);this.notes.emit('project-lifecycle',{boxPath:op.boxPath,state:op.state,operation:op.kind,project:op.project});this.notes.startWatch();return op;
    });
  }
}
