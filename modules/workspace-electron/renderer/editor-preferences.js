export const editorDefaults={fontSize:16,lineHeight:1.7,width:820,propertiesCollapsed:false};
const clamp=(value,min,max,fallback)=>Number.isFinite(Number(value))?Math.max(min,Math.min(max,Number(value))):fallback;
export function editorPreferences(value={}){return {fontSize:clamp(value.fontSize,12,26,16),lineHeight:clamp(value.lineHeight,1.3,2.2,1.7),width:clamp(value.width,540,1400,820),propertiesCollapsed:value.propertiesCollapsed===true};}
export function loadEditorPreferences(storage){try{return editorPreferences(JSON.parse(storage.getItem('editor-preferences')||'{}'));}catch{return {...editorDefaults};}}
export function applyEditorPreferences(value,root=document.documentElement){
  const prefs=editorPreferences(value);root.style.setProperty('--editor-text-size',`${prefs.fontSize}px`);root.style.setProperty('--editor-line-height',String(prefs.lineHeight));root.style.setProperty('--editor-readable-width',`${prefs.width}px`);root.dataset.propertiesCollapsed=String(prefs.propertiesCollapsed);return prefs;
}
export function saveEditorPreferences(storage,value){const prefs=editorPreferences(value);try{storage.setItem('editor-preferences',JSON.stringify(prefs));return true;}catch{return false;}}
export const shortcuts=[
 ['Appearance','Zoom in / out','Mod+Plus / Mod+Minus'],['Appearance','Reset interface zoom','Mod+0'],
 ['Navigation','Command palette','Mod+P'],['Navigation','Search files','Mod+O'],['Navigation','Back / forward','Mod+[ / Mod+]'],['Navigation','Toggle sidebar','Mod+\\'],
 ['Tabs','New tab','Mod+T'],['Tabs','Close tab','Mod+W'],['Tabs','Reopen closed tab','Mod+Shift+T'],['Tabs','Next / previous tab','Ctrl+Tab / Ctrl+Shift+Tab'],['Tabs','Cycle tabs','Mod+Alt+Left / Right'],['Tabs','Open link in a new tab','Mod+Click'],['Tabs','Open link in background','Middle click / Mod+Shift+Click'],
 ['Editing','New note','Mod+N'],['Editing','Save','Mod+S'],['Editing','Find','Mod+F'],['Editing','Bold / italic','Mod+B / Mod+I'],['Files','Rename selected entry','F2'],['Files','Copy / cut / paste','Mod+C / Mod+X / Mod+V'],['Settings','Open settings','Mod+,'],
];
