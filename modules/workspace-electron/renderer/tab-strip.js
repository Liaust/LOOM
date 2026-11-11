export function tabWheelDelta(event,width){
  if(event.ctrlKey||event.metaKey)return 0;
  const delta=Math.abs(event.deltaX)>Math.abs(event.deltaY)?event.deltaX:event.deltaY;
  return delta*(event.deltaMode===1?24:event.deltaMode===2?width:1);
}
export class TabStrip{
  constructor(dom,previous,next){
    Object.assign(this,{dom,previous,next});
    dom.addEventListener('wheel',event=>{if(dom.scrollWidth<=dom.clientWidth)return;const delta=tabWheelDelta(event,dom.clientWidth);if(delta){event.preventDefault();dom.scrollLeft+=delta;this.update();}},{passive:false});
    previous.onclick=()=>dom.scrollBy({left:-Math.max(160,dom.clientWidth*.7),behavior:'smooth'});
    next.onclick=()=>dom.scrollBy({left:Math.max(160,dom.clientWidth*.7),behavior:'smooth'});
    dom.addEventListener('scroll',()=>this.update(),{passive:true});
    this.resize=new ResizeObserver(()=>this.update());this.resize.observe(dom);
  }
  update(){const overflow=this.dom.scrollWidth>this.dom.clientWidth+1;this.previous.hidden=this.next.hidden=!overflow;this.previous.disabled=this.dom.scrollLeft<1;this.next.disabled=this.dom.scrollLeft+this.dom.clientWidth>=this.dom.scrollWidth-1;}
  render(active){const changed=this.active!==active;this.active=active;if(changed)this.dom.querySelector('[aria-selected=true]')?.scrollIntoView({block:'nearest',inline:'nearest'});this.update();}
}
