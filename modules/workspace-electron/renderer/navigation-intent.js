export function navigationIntent(event={}) {
  const modified=Boolean(event.metaKey||event.ctrlKey),middle=event.button===1;
  return {newTab:modified||middle,background:middle||modified&&Boolean(event.shiftKey)};
}
export function bindNavigation(element,run){
  element.onclick=event=>{event.preventDefault();run(navigationIntent(event),event);};
  element.onauxclick=event=>{if(event.button===1){event.preventDefault();run(navigationIntent(event),event);}};
  return element;
}
