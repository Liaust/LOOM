import path from 'node:path';

// Local replica keys bind a queue entry, but are not fields of Main's note API.
// Construct the owner request explicitly rather than spreading a Box descriptor.
export function directoryMutation(binding,snapshot,destination,id){
  if(!snapshot.file?.file_id||!snapshot.base_id||!snapshot.generation||snapshot.path!==destination||snapshot.root!==binding.root)throw Error('Main returned an unrelated attachment-folder base.');
  return {root:binding.root,...binding.box_id?{box_id:binding.box_id}:{},path:destination,operation_id:id,file_id:snapshot.file.file_id,base_id:snapshot.base_id,generation:snapshot.generation,kind:'edit',text:'',directory:true};
}
export async function ensureAttachmentDirectory(remote,v,binding){
  const folder=path.posix.dirname(v.destination),snapshot=(await remote.json('note',{root:binding.root,path:folder,directory:true})).data;
  if(snapshot.exists)return;
  const result=(await remote.json('note',{}, {method:'POST',body:directoryMutation(binding,snapshot,folder,v.id+'-folder')})).data;
  if(result.operation?.state!=='accepted'&&result.state!=='accepted')throw Error('Attachment folder is awaiting source acceptance.');
}
