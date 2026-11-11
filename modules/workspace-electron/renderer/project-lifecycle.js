import {form,confirmAction} from './dialogs.js';

export async function reviewProjectLifecycle({api,project,operation,flush,notice,changed,saved}) {
  if(!saved||saved.state==='preview')await flush();
  const op=saved||await api.action({kind:'project-lifecycle-preview',ref:project,operation});
  const plan=op.plan,w=plan.workspace;
  const description=[`${w.source.root}/${w.source.relative_path}`,`→ ${w.destination.root}/${w.destination.relative_path}`,'',`${w.inventory_entries} entries · ${w.inventory_bytes.toLocaleString()} bytes`,`${plan.deactivation_actions} runtime actions across ${plan.deactivation_facets} facets.`,op.kind==='restore'?'Restored services and schedules remain inactive.':'Services and schedules are quiesced before the physical move.','Local notes and recovery history are preserved.',`Operation: ${w.operation_id}`,`State: ${op.state}`,op.receipt?`Source phase: ${op.receipt.phase||'Unknown'}`:'',op.error||''].filter(x=>x!==undefined).join('\n');
  if(op.state==='complete'){await form('Project operation complete',[],{submit:'Close',description});return op;}
  if(op.state==='rejected'){await form('Project review expired',[],{submit:'Close',description:`${description}\n\nMain rejected this review before changing the project. Start a fresh review.`});return op;}
  const recover=op.state!=='preview';
  if(!await confirmAction(recover?'Review saved project operation':op.kind==='restore'?'Restore project?':'Archive project?',description,recover?'Recover this exact operation':op.kind==='restore'?'Restore inactive':'Archive project'))return;
  if(!recover)await flush();
  const result=await api.action({kind:recover?'project-lifecycle-recover':'project-lifecycle-apply',id:op.id});
  if(result.state==='complete'){await changed(result);notice(result.kind==='restore'?'Project restored · runtime inactive':'Project archived · local copies preserved');}
  else await form('Project operation needs review',[],{submit:'Close',description:`${result.error||'Main retained an incomplete operation.'}\n\nOperation: ${w.operation_id}\nSource phase: ${result.receipt?.phase||'Not confirmed'}\n\nUse Saved project operations to inspect and recover this exact request. No automatic replay was started.`});
  return result;
}
