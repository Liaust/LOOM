// Runtime services outlive a Mac window; only explicit quit drains their owner.
export class WindowLifecycle{
 constructor({create,flush,closed=()=>{},failed=()=>{}}){Object.assign(this,{create,flush,closed,failed});this.allowed=new WeakSet();this.pending=new WeakSet();}
 async open(){
  if(this.window&&!this.window.isDestroyed()){this.window.show();this.window.focus();return this.window;}
  if(this.opening)return this.opening;
  this.opening=Promise.resolve().then(()=>this.create()).then(w=>{
   this.window=w;w.on('close',event=>{if(this.allowed.has(w))return;event.preventDefault();if(this.pending.has(w))return;this.pending.add(w);
    Promise.resolve().then(()=>this.flush(w)).then(()=>{this.allowed.add(w);if(!w.isDestroyed())w.close();}).catch(e=>this.failed(w,e)).finally(()=>this.pending.delete(w));
   });w.on('closed',()=>{if(this.window===w)this.window=null;this.closed(w);});return w;
  }).finally(()=>{this.opening=null;});return this.opening;
 }
 allowClose(){if(this.window&&!this.window.isDestroyed())this.allowed.add(this.window);}
}
