import {getDocument,GlobalWorkerOptions,AnnotationMode,AnnotationEditorType,PDFDataRangeTransport} from 'pdfjs-dist/build/pdf.mjs';
import {EventBus,PDFViewer,PDFLinkService,PDFFindController} from 'pdfjs-dist/web/pdf_viewer.mjs';
import {fetchPdfRange} from '../src/pdf-range.mjs';

GlobalWorkerOptions.workerSrc = new URL('./pdf.worker.mjs',import.meta.url).href;
const element = (tag,className,text) => {
  const value=document.createElement(tag);value.className=className||'';
  if(text!==undefined)value.textContent=text;
  return value;
};

// PDF.js owns parsing, text/annotation layers, search and visible-page rendering.
// LOOM owns the controls and the existing revision-bound, ranged source URL.
export class PdfViewer {
  constructor({url,size,name,icon,notice,external,download,copyPage,form,page=1,compact=false,onPage=()=>{},onReady=()=>{}}) {
    Object.assign(this,{url,size,name,icon,notice,external,download,copyPage,form,onPage,onReady});
    this.initialPage=page;this.abort=new AbortController();this.bus=new EventBus();
    this.dom=element('div','loom-pdf');this.tools=element('div','view-controls pdf-controls');
    this.body=element('div','pdf-body');this.outline=element('nav','pdf-outline');this.outline.hidden=true;
    this.outline.setAttribute('aria-label','PDF outline');
    this.container=element('div','pdf-scroll');this.pages=element('div','pdfViewer');
    this.container.tabIndex=0;this.container.setAttribute('aria-label',name);
    this.container.append(this.pages);this.body.append(this.outline,this.container);
    this.message=element('div','pdf-message','Opening PDF…');this.message.setAttribute('role','status');
    this.body.append(this.message);this.dom.append(this.tools,this.body);
    this.previous=this.button('Previous page','chevron-up',()=>this.go(this.viewer.currentPageNumber-1));
    this.next=this.button('Next page','chevron-down',()=>this.go(this.viewer.currentPageNumber+1));
    this.pageInput=element('input','pdf-page-number');this.pageInput.type='text';this.pageInput.inputMode='numeric';
    this.pageInput.setAttribute('aria-label','PDF page');this.pageInput.value='1';this.pageCount=element('span','pdf-page-count','/ —');
    this.pageInput.onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();this.go(Number(this.pageInput.value));this.container.focus();}};
    this.pageInput.onblur=()=>this.syncPage();
    this.zoomLabel=this.button('Reset PDF zoom','',()=>this.scale(1));this.zoomLabel.textContent='100%';this.zoomLabel.classList.add('pdf-zoom');
    this.outlineButton=this.button('Toggle PDF outline','panel-left',()=>this.toggleOutline());
    this.findButton=this.button('Find in PDF','search',()=>this.toggleFind());
    this.tools.append(this.outlineButton,this.previous,this.pageInput,this.pageCount,this.next,
      element('span','pdf-controls-gap'),this.button('Zoom out','minus',()=>this.scale(this.viewer.currentScale/1.2)),
      this.zoomLabel,this.button('Zoom in','plus',()=>this.scale(this.viewer.currentScale*1.2)),
      this.button('Fit page width','move-horizontal',()=>this.fit('page-width')),
      this.button('Fit whole page','scan',()=>this.fit('page-fit')),
      this.button('Rotate PDF clockwise','rotate-cw',()=>{this.viewer.pagesRotation=(this.viewer.pagesRotation+90)%360;}),
      this.findButton);
    if(copyPage)this.tools.append(this.button('Copy PDF page link','link',()=>copyPage(this.viewer.currentPageNumber)));
    if(download)this.tools.append(this.button('Save original PDF copy','download',download,false));
    if(compact){this.dom.classList.add('pdf-compact');for(const button of this.tools.querySelectorAll('button'))if(['Toggle PDF outline','Fit whole page','Rotate PDF clockwise','Find in PDF'].includes(button.getAttribute('aria-label')))button.hidden=true;}
    this.findBar=element('div','pdf-find');this.findBar.hidden=true;
    this.findInput=element('input');this.findInput.placeholder='Find in PDF';this.findInput.setAttribute('aria-label','Find in PDF');
    this.findCount=element('span','pdf-find-count');
    this.findInput.oninput=()=>this.find();
    this.findInput.onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();this.find(true,event.shiftKey);}else if(event.key==='Escape'){event.stopPropagation();this.toggleFind(false);}};
    this.findBar.append(this.findInput,this.findCount,this.button('Previous match','chevron-up',()=>this.find(true,true)),
      this.button('Next match','chevron-down',()=>this.find(true)),this.button('Close PDF search','x',()=>this.toggleFind(false)));
    this.dom.insertBefore(this.findBar,this.body);
    this.dom.addEventListener('keydown',event=>{
      if((event.metaKey||event.ctrlKey)&&event.key.toLowerCase()==='f'){event.preventDefault();event.stopPropagation();this.toggleFind(true);}
    });
    this.bus.on('pagesinit',()=>{
      if(this.destroyed)return;this.fit('page-width');this.go(this.initialPage);this.syncPage();
    });
    this.bus.on('pagechanging',()=>this.syncPage());
    this.bus.on('scalechanging',({scale})=>{this.zoomLabel.textContent=`${Math.round(scale*100)}%`;});
    this.bus.on('pagerendered',({error})=>{if(this.destroyed)return;if(error)this.fail(error);else{this.message.hidden=true;if(!this.ready){this.ready=true;this.onReady(performance.now()-this.startedAt);}}});
    this.bus.on('updatefindmatchescount',({matchesCount})=>{this.findCount.textContent=matchesCount.total?`${matchesCount.current} / ${matchesCount.total}`:'';});
    this.bus.on('updatefindcontrolstate',({state})=>{if(state===1)this.findCount.textContent='No matches';else if(state===3)this.findCount.textContent='Searching…';});
  }
  button(label,symbol,action,requiresDocument=true) {
    const button=element('button','icon');button.type='button';button.setAttribute('aria-label',label);button.dataset.tooltip=label;
    if(symbol)button.append(this.icon(symbol));
    button.onclick=()=>Promise.resolve().then(()=>{if(this.document||!requiresDocument)return action();}).catch(error=>this.notice(error.message));
    return button;
  }
  async mount() {
    if(this.started)return;this.started=true;this.startedAt=performance.now();
    try {
      this.links=new PDFLinkService({eventBus:this.bus});
      this.links.addLinkAttributes=(link,url)=>{
        link.href='#';link.onclick=event=>{event.preventDefault();Promise.resolve(this.external(url)).catch(error=>this.notice(error.message));};
      };
      this.findController=new PDFFindController({eventBus:this.bus,linkService:this.links});
      this.viewer=new PDFViewer({container:this.container,viewer:this.pages,eventBus:this.bus,
        linkService:this.links,findController:this.findController,abortSignal:this.abort.signal,
        annotationMode:AnnotationMode.ENABLE,annotationEditorMode:AnnotationEditorType.DISABLE,
        maxCanvasPixels:8*1024*1024,maxCanvasDim:8192,enableDetailCanvas:false,
        imageResourcesPath:new URL('./pdf-images/',import.meta.url).href});
      this.links.setViewer(this.viewer);
      this.rangeAbort=new AbortController();
      this.range=new PDFDataRangeTransport(this.size,new Uint8Array(),true);
      this.range.requestDataRange=(begin,end)=>{
        fetchPdfRange(this.url,begin,end,this.size,{signal:this.rangeAbort.signal})
          .then(bytes=>{if(!this.destroyed)this.range.onDataRange(begin,bytes);})
          .catch(error=>{if(!this.destroyed){this.fail(error);Promise.resolve(this.loading?.destroy()).catch(()=>{});}});
      };
      this.range.abort=()=>this.rangeAbort.abort();
      this.loading=getDocument({range:this.range,length:this.size,disableStream:true,disableAutoFetch:true,rangeChunkSize:256*1024,
        isEvalSupported:false,enableXfa:false,cMapUrl:new URL('./pdf-cmaps/',import.meta.url).href,cMapPacked:true,
        standardFontDataUrl:new URL('./pdf-fonts/',import.meta.url).href,wasmUrl:new URL('./pdf-wasm/',import.meta.url).href});
      this.loading.onPassword=async(update,reason)=>{
        if(this.destroyed)return;
        const choice=await this.form(reason===2?'Incorrect PDF password':'Password-protected PDF',[
          {name:'password',label:'Password',type:'password',required:true}],{submit:'Open PDF'});
        if(this.destroyed)return;
        if(choice)update(choice.password);else{this.fail(Error('PDF opening cancelled. The original is unchanged.'));await this.loading.destroy();}
      };
      this.document=await this.loading.promise;
      if(this.destroyed)return;
      this.links.setDocument(this.document);this.viewer.setDocument(this.document);
      this.outlineButton.disabled=false;this.syncPage();
      this.resize=new ResizeObserver(()=>{if(this.fitMode&&this.document&&!this.destroyed)this.viewer.currentScaleValue=this.fitMode;});
      this.resize.observe(this.container);
    } catch(error) {if(!this.destroyed)this.fail(error);}
  }
  fail(error) {
    if(this.failed)return;this.failed=true;
    this.message.hidden=false;this.message.textContent=`PDF could not be displayed: ${error.message||error}. The original is unchanged.`;
    this.message.classList.add('error');
  }
  syncPage() {
    if(!this.document||this.destroyed)return;
    const page=this.viewer.currentPageNumber;
    if(document.activeElement!==this.pageInput)this.pageInput.value=String(page);
    this.pageCount.textContent=`/ ${this.document.numPages}`;
    this.previous.disabled=page<=1;this.next.disabled=page>=this.document.numPages;this.onPage(page);
  }
  go(number) {
    if(!this.document){if(Number.isSafeInteger(number)&&number>0)this.initialPage=number;return;}
    if(!this.document||!Number.isInteger(number)||number<1||number>this.document.numPages){this.syncPage();return;}
    this.viewer.currentPageNumber=number;this.syncPage();
  }
  fit(value) {this.fitMode=value;this.viewer.currentScaleValue=value;}
  scale(value) {this.fitMode=null;this.viewer.currentScale=Math.max(.2,Math.min(5,value));}
  toggleFind(open=this.findBar.hidden) {
    this.findBar.hidden=!open;this.findButton.setAttribute('aria-pressed',String(open));
    if(open){this.findInput.focus();this.findInput.select();}else{this.bus.dispatch('findbarclose',{source:this});this.container.focus();}
  }
  find(again=false,backward=false) {
    if(!this.document)return;
    this.bus.dispatch('find',{source:this,type:again?'again':'',query:this.findInput.value,phraseSearch:true,
      caseSensitive:false,entireWord:false,highlightAll:true,findPrevious:backward,matchDiacritics:false});
  }
  async toggleOutline() {
    this.outline.hidden=!this.outline.hidden;this.outlineButton.setAttribute('aria-expanded',String(!this.outline.hidden));
    if(this.outline.hidden||this.outlineLoaded)return;
    this.outline.textContent='Loading outline…';
    const items=await this.document.getOutline();if(this.destroyed)return;
    this.outlineLoaded=true;this.outline.replaceChildren();let count=0;
    const add=(rows,depth)=>{for(const row of rows||[]){
      if(++count>1000||depth>20)return;
      const button=element('button','pdf-outline-item',row.title||'Untitled');button.style.paddingLeft=`${12+depth*14}px`;
      button.onclick=()=>Promise.resolve(row.url?this.external(row.url):this.links.goToDestination(row.dest)).catch(error=>this.notice(error.message));
      this.outline.append(button);add(row.items,depth+1);
    }};
    add(items,0);if(!count)this.outline.append(element('p','pdf-outline-empty','No document outline'));
  }
  flush() { /* Read-only PDF views have no edits to flush. */ }
  destroy() {
    if(this.destroyed)return;this.destroyed=true;this.resize?.disconnect();this.abort.abort();
    this.viewer?.setDocument(null);this.links?.setDocument(null);this.findController?.setDocument(null);
    Promise.resolve(this.loading?.destroy()).catch(()=>{});this.pages.replaceChildren();
  }
}
