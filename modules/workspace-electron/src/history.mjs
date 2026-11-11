export function historyRange(from,to){
 const boundary=(value,next=false)=>{if(!/^\d{4}-\d{2}-\d{2}$/.test(value))throw Error('Choose a valid timeline date.');const [y,m,d]=value.split('-').map(Number),date=new Date(y,m-1,d);if(date.getFullYear()!==y||date.getMonth()!==m-1||date.getDate()!==d)throw Error('Choose a valid timeline date.');if(next)date.setDate(date.getDate()+1);return date.toISOString();};
 const result={...from?{from:boundary(from)}:{},...to?{to:boundary(to,true)}:{}};if(from&&to&&to<from)throw Error('The end date must not precede the start date.');return result;
}
export class WorkspaceHistory{
 constructor(remote,notes,cache){Object.assign(this,{remote,notes,cache});}
 async read(input){
  const query={root:input.root,path:input.path||'',action:input.action==='compare'?'compare':'list',...input.after?{after:input.after}:{},...input.kind?{kind:input.kind}:{},...input.operation?{operation:input.operation}:{},...input.from?{from:input.from}:{},...input.to?{to:input.to}:{},limit:100},key=JSON.stringify(['history',query]);let result,origin='main';
  try{result=(await this.remote.json('history',query)).data;this.cache.remember(key,result);}catch(error){result=this.cache.cached(key);if(!result)throw error;origin='cached';result={...result,error:error.message};}
  if(query.action==='list'&&!input.after){const local=this.notes.pendingHistory(query);const ids=new Set(local.map(i=>i.id));result={...result,items:[...local,...result.items.filter(i=>!ids.has(i.id))]};}
  return{...result,origin};
 }
}
