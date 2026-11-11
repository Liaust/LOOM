export class ContextMenu {
  constructor(container,{icon,failed}){
    Object.assign(this,{container,icon,failed});
    document.addEventListener('pointerdown',event=>{if(!container.hidden&&!container.contains(event.target))this.close(false);},true);
    window.addEventListener('blur',()=>this.close(false));
    container.addEventListener('keydown',event=>{
      const buttons=[...container.querySelectorAll('button:not(:disabled)')],index=buttons.indexOf(document.activeElement);let next;
      if(event.key==='ArrowDown')next=buttons[(index+1)%buttons.length];if(event.key==='ArrowUp')next=buttons[(index-1+buttons.length)%buttons.length];if(event.key==='Home')next=buttons[0];if(event.key==='End')next=buttons.at(-1);
      if(next){event.preventDefault();next.focus();}if(event.key==='Escape'){event.preventDefault();event.stopPropagation();this.close();}
    });
  }
  close(restore=true){this.container.hidden=true;if(restore&&this.previous?.isConnected)this.previous.focus({preventScroll:true});}
  show(actions,position){
    this.previous=document.activeElement;this.container.replaceChildren();
    for(const action of actions){if(action.separator){this.container.append(document.createElement('hr'));continue;}
      const button=document.createElement('button');button.type='button';button.setAttribute('role','menuitem');button.append(this.icon(action.icon||'file'),document.createTextNode(action.label));if(action.danger)button.classList.add('danger');
      button.disabled=Boolean(action.disabled);if(action.hint)button.dataset.tooltip=action.hint;
      button.onclick=()=>{if(action.disabled)return;this.close();Promise.resolve().then(action.run).catch(error=>this.failed(error.message));};this.container.append(button);
    }
    this.container.hidden=false;
    this.container.style.left=`${Math.max(8,Math.min(position.x,innerWidth-this.container.offsetWidth-8))}px`;
    this.container.style.top=`${Math.max(8,Math.min(position.y,innerHeight-this.container.offsetHeight-8))}px`;
    this.container.querySelector('button')?.focus();
  }
}
