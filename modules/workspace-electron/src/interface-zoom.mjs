import {readFile,writeFile,rename} from 'node:fs/promises';

export function zoomShortcut(input,platform=process.platform){
  if(input.type!=='keyDown'||input.alt||!(platform==='darwin'?input.meta:input.control))return null;
  if(['+','='].includes(input.key)||input.code==='NumpadAdd')return 'in';
  if(['-','_'].includes(input.key)||input.code==='NumpadSubtract')return 'out';
  if(input.key==='0'&&!input.shift)return 'reset';
  return null;
}
export const zoomFactor=value=>Number.isFinite(value)?Math.round(Math.max(.5,Math.min(2,value))*100)/100:1;
export class InterfaceZoom{
  constructor(file,changed=()=>{}){this.file=file;this.changed=changed;this.factor=1;this.pending=Promise.resolve();}
  async init(){try{this.factor=zoomFactor(JSON.parse(await readFile(this.file,'utf8')).zoom);}catch{}return this;}
  async change(action,contents){
    if(!['in','out','reset','status'].includes(action))throw Error('Unknown interface zoom action.');
    if(action==='status')return {factor:this.factor};
    const next=zoomFactor(action==='reset'?1:this.factor+(action==='in'?.1:-.1));
    if(next===this.factor)return {factor:next};
    this.factor=next;contents?.setZoomFactor(next);this.changed({factor:next});
    this.pending=this.pending.catch(()=>{}).then(async()=>{await writeFile(this.file+'.tmp',JSON.stringify({zoom:next}),{mode:0o600});await rename(this.file+'.tmp',this.file);});
    await this.pending;return {factor:next};
  }
}
