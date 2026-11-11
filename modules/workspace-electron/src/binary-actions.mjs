import {randomUUID} from 'node:crypto';
import path from 'node:path';
import {normalize} from './local-files.mjs';
import {savedRoot,sameBoxRoots} from './box-roots.mjs';

// Metadata-only requests share the native durable outbox. No cache miss or
// remote-only asset action can turn into an implicit payload download.
export class BinaryActions {
  constructor(owner,cache){this.owner=owner;this.cache=cache;}
  async mutate({root,box_id,target_root=root,path:relative,operation,target,name}){
    sameBoxRoots(root,target_root);const binding=this.owner.boxes.identity(root,box_id);
    if(!['rename','trash'].includes(operation))throw Error('Binary actions support Box rename and recovery removal only.');
    relative=normalize(relative);const destination=operation==='rename'?normalize(target||path.posix.join(path.posix.dirname(relative),name||'')):undefined;
    if(!relative||destination===relative)throw Error('Choose a different visible file destination.');
    const owner=this.owner;owner.trees.assertAvailable(relative,root);if(destination)owner.trees.assertAvailable(destination,root);
    const {data:source}=await owner.remote.json('actions',{root,path:relative});
    if(source.path!==relative||!source.file?.file_id||!source.base_id||!source.generation)throw Error('Main did not return a complete metadata observation.');
    if(source.root&&source.root!==root)throw Error('Main returned an unrelated metadata root.');
    const op={id:`desktop_action_${randomUUID().replaceAll('-','')}`,...binding,kind:'metadata_action',action:operation==='trash'?'delete':'rename',path:relative,target:destination,
      fileKey:'remote_asset',state:'queued',mainFirst:true,at:new Date().toISOString(),placement:{stage:'waiting'}};
    op.request={root,path:relative,operation_id:op.id,file_id:source.file.file_id,base_id:source.base_id,generation:source.generation,kind:op.action,...(destination?{target:destination}:{})};
    await owner.change(()=>{
      if(owner.suspended)throw Error('Recovery capture is in progress.');
      for(const row of owner.db.prepare("SELECT record FROM native_operations WHERE state IN ('queued','pending','held') AND json_extract(record,'$.kind')='metadata_action'").all()){
        const other=JSON.parse(row.record);if(savedRoot(other)===root&&[other.path,other.target].filter(Boolean).some(p=>p===relative||p===destination))throw Error('An existing asset action needs acceptance or review first.');
      }
      owner.insertOp(op);
    });owner.pump();return {...binding,path:relative,target:destination,operation_id:op.id,status:'pending_main'};
  }
  async send(op){
    const owner=this.owner;let result;
    try{result=(await owner.remote.json('actions',owner.boxes.identity(savedRoot(op),op.box_id), {method:'POST',body:op.request})).data;}
    catch(error){if(error.status>=400&&error.status<500){op.state='held';op.reason=error.message;owner.saveOp(op);owner.emit('sync-error',{error:op.reason});return;}throw error;}
    if(result.operation_id!==op.id||!['accepted','conflict','held','pending'].includes(result.state))throw Error('Main returned an invalid asset-action receipt.');
    if(result.state==='accepted'&&(result.file?.file_id!==op.request.file_id||result.path!==(op.target||op.path)||!result.base_id||result.root&&result.root!==savedRoot(op)))throw Error('Main returned an unrelated asset receipt.');
    await owner.change(async()=>{
      op.receipt=result;op.state=result.state;op.reason=result.reason;owner.saveOp(op);
      if(op.state==='accepted'){
        await this.finish(op);
      }else if(op.state==='pending')owner.retry();
      else owner.emit('sync-error',{error:op.reason||'The asset action needs source-owner review.'});
    });
  }
  async finish(op){
    if(op.placement.stage==='done')return;
    try{
      const binding=this.owner.boxes.identity(savedRoot(op),op.box_id);
      await this.cache.retarget(binding.root,op.path,op.target,op.id);
      await this.owner.emit('asset-sync',{...binding,previousPath:op.path,path:op.target||op.path,deleted:op.action==='delete',operation_id:op.id,pathAccepted:true});
      op.placement.stage='done';delete op.placement.error;this.owner.saveOp(op);
    }catch(error){op.placement.error=error.message;this.owner.saveOp(op);this.owner.emit('sync-error',{error:`Main accepted the asset action; local references need recovery: ${error.message}`});}
  }
  async init(){for(const row of this.owner.db.prepare("SELECT record FROM native_operations WHERE state='accepted' AND json_extract(record,'$.kind')='metadata_action' AND json_extract(record,'$.placement.stage')<>'done'").all())await this.finish(JSON.parse(row.record));}
}
