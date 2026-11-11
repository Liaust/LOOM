export const themeOptions=Object.freeze([
  Object.freeze({id:'graphite-jade',name:'Graphite + Jade'}),
  Object.freeze({id:'coffee-cream',name:'Coffee & Cream'}),
]);
export const defaultTheme='graphite-jade';
export const themeKey='workspace-theme';
export const themeID=value=>themeOptions.some(theme=>theme.id===value)?value:defaultTheme;
export function loadTheme(storage){try{return themeID((storage||globalThis.localStorage).getItem(themeKey));}catch{return defaultTheme;}}
export function saveTheme(id,storage){
  if(!themeOptions.some(theme=>theme.id===id))throw Error('Unknown workspace theme');
  try{(storage||globalThis.localStorage).setItem(themeKey,id);return true;}catch{return false;}
}
export function applyTheme(document,id){
  const value=themeID(id);document.documentElement.dataset.theme=value;
  const window=document.defaultView;
  if(window)window.dispatchEvent(new window.CustomEvent('workspace-theme-changed',{detail:{id:value}}));
  return value;
}
