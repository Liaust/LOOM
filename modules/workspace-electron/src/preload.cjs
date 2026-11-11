const { contextBridge, ipcRenderer, webUtils } = require('electron');
const call = name => async input => {
  const result = await ipcRenderer.invoke(`workspace:${name}`, input);
  if (!result.ok) throw new Error(result.error);
  return result.data;
};
contextBridge.exposeInMainWorld('workspace', {
  state: call('state'), connect: call('connect'), list: call('list'), open: call('open'),zoom:call('zoom'),
  previewText:call('preview-text'),
  watchDirectories:call('watch-directories'),
  attachments:call('attachments'),attachmentPreview:call('attachment-preview'),releaseAttachment:call('attachment-release'),
  attachDrop:(files,target)=>call('attachments')({...target,action:'drop',sources:Array.from(files,file=>webUtils.getPathForFile(file)).filter(Boolean)}),
  applicationData:call('application-data'),
  downloadFile:call('download-file'),
  sendMac:call('send-mac'),
  directoryCopy:call('directory-copy'),
  save: call('save'), draft: call('draft'), pin: call('pin'), chooseLocal: call('choose-local'), external: call('external'),
  annotations: call('annotations'), explain: call('explain'), overview: call('overview'), dashboard:call('dashboard'), inspect: call('inspect'), action: call('action'), cancelOverview: call('cancel-overview'),
  mutate: call('mutate'), search: call('search'),find:call('find'), enrich: call('enrich'),
  resolveReference:call('resolve-reference'),baseQuery:call('base-query'),
  noteGraph:call('note-graph'),
  knowledgeCatalog:call('knowledge-catalog'),baseDefinition:call('base-definition'),
  history:call('history'),
  offline:call('offline'),
  uploads:call('uploads'),uploadPick:call('upload-pick'),
  metadataRefresh:call('metadata-refresh'),availability:call('availability'),
  updates:call('updates'),
  uploadDrop:(files,target)=>call('upload-drop')({...target,sources:Array.from(files,file=>webUtils.getPathForFile(file)).filter(Boolean)}),
  paths:call('paths'),copyPath:call('copy-path'),revealLocal:call('reveal-local'),
  reviewNote:call('note-review'),resolveNote:call('note-resolve'),
  onEvent: callback => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on('workspace:event', listener);
    return () => ipcRenderer.removeListener('workspace:event', listener);
  },
});
