import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
const exec=promisify(execFile),TAG='workspace-stable';
const configuredRepository=()=>process.env.LOOM_WORKSPACE_UPDATE_REPOSITORY||'';
const validRepository=value=>typeof value==='string'&&/^[A-Za-z0-9][A-Za-z0-9-]*\/[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(value);
function immutableAsset(value,repository){try{const url=new URL(value);return validRepository(repository)&&url.origin==='https://api.github.com'&&!url.username&&!url.password&&!url.search&&!url.hash&&url.pathname.startsWith(`/repos/${repository}/releases/assets/`)&&/^[1-9][0-9]*$/.test(url.pathname.slice(`/repos/${repository}/releases/assets/`.length));}catch{return false;}}
const versionParts=v=>/^\d+\.\d+\.\d+$/.test(v||'')?v.split('.').map(Number):null;
export function newerVersion(candidate,current){const a=versionParts(candidate),b=versionParts(current);if(!a||!b||[...a,...b].some(v=>!Number.isSafeInteger(v)))return false;for(let i=0;i<3;i++){if(a[i]!==b[i])return a[i]>b[i];}return false;}
export function validateUpdateFeed(raw,{version,arch,platform='darwin',requireNewer=true,repository=configuredRepository()}){
  if(!raw||raw.component!=='loom-workspace'||raw.channel!=='stable'||raw.platform!==platform||raw.arch!==arch||!versionParts(raw.version)||(requireNewer&&!newerVersion(raw.version,version))||!/^[0-9a-f]{40}$/.test(raw.source||'')||!/^[0-9a-f]{64}$/.test(raw.sha256||'')||!Number.isSafeInteger(raw.size)||raw.size<1||raw.size>2*1024**3||typeof raw.notes!=='string'||raw.notes.length>12000||!immutableAsset(raw.url,repository))throw Error('The update feed is incompatible or invalid.');
  return {version:raw.version,source:raw.source,sha256:raw.sha256,size:raw.size,notes:raw.notes,url:raw.url};
}
export class AppUpdates {
  constructor({native,build,version,arch=process.arch,platform=process.platform,repository=configuredRepository(),emit=()=>{},load,authorize,prepareInstall}){
    Object.assign(this,{native,build,version,arch,platform,repository,emit,load,authorize,prepareInstall});this.state='setup';this.message='A signed release channel has not been configured.';
    native.on('error',()=>this.change('failed','Update download failed. The installed app and local work are unchanged.'));
    native.on('update-not-available',()=>this.change('current','No compatible update is available.'));
    native.on('update-downloaded',()=>this.change('ready','Ready to install. This downloaded update may also install on the next app launch.'));
  }
  status(){return {version:this.version,source:this.build.source||'',state:this.state,message:this.message,available:this.candidate?{version:this.candidate.version,notes:this.candidate.notes}:null};}
  change(state,message){this.state=state;this.message=message;this.emit('app-update',this.status());return this.status();}
  async check(){
    if(this.checking)return this.checking;
    if(['downloading','ready','installing'].includes(this.state))return this.status();
    if(this.platform!=='darwin'||this.build.kind!=='signed-notarized')return this.change('setup','This development build is not signed for native updates. Apple setup and the first signed release remain pending.');
    if(!validRepository(this.repository))return this.change('setup','Configure an operator-owned signed release repository before checking for updates.');
    this.checking=(async()=>{this.change('checking','Checking the configured Loom release…');try{
      const {feed,feedURL}=await this.load();
      validateUpdateFeed(feed,{...this,requireNewer:false});
      if(!immutableAsset(feedURL,this.repository))throw Error('Invalid immutable feed URL.');
      if(!newerVersion(feed.version,this.version)){this.candidate=null;return this.change('current','You have the current Loom version.');}
      this.candidate=validateUpdateFeed(feed,this);this.feedURL=feedURL;return this.change('available',`Version ${this.candidate.version} is available.`);
    }catch{this.candidate=null;return this.change('failed','The configured update feed could not be verified. Retry after checking release access.');}})().finally(()=>{this.checking=null;});return this.checking;
  }
  async download(){
    if(this.state!=='available'||!this.candidate||this.build.kind!=='signed-notarized')throw Error('No verified signed update is selected.');
    this.change('downloading','Downloading the selected signed update…');
    try{const headers=await this.authorize();this.native.setFeedURL({url:this.feedURL,headers});this.native.checkForUpdates();return this.status();}catch{this.change('failed','Native update authorization failed. No replacement was requested.');throw Error(this.message);}
  }
  async install(){
    if(this.state!=='ready')throw Error('Download a verified update first.');
    this.change('installing','Preserving local work before restart…');
    try{await this.prepareInstall();}catch{this.change('ready','Restart held: local work could not be fully preserved.');throw Error(this.message);}
    this.native.quitAndInstall();return this.status();
  }
}

// Existing local authorization only; no credential is persisted or returned to
// the renderer. Each selected GitHub asset ID is immutable, unlike a latest URL.
export function privateReleaseAccess({arch,appPath,repository=configuredRepository()}){
  const gh='/opt/homebrew/bin/gh';
  const run=async args=>(await exec(gh,args,{timeout:15000,maxBuffer:256*1024})).stdout;
  return {
    load:async()=>{
      if(!validRepository(repository))throw Error('No signed release repository configured.');
      const release=JSON.parse(await run(['api',`repos/${repository}/releases/tags/${TAG}`]));
      if(release.tag_name!==TAG||release.draft||release.prerelease)throw Error('No stable Loom feed.');
      const asset=release.assets?.find(a=>a.name===`workspace-darwin-${arch}-stable.json`);
      if(!asset||!Number.isSafeInteger(asset.id)||asset.size>32768)throw Error('No bounded Loom feed.');
      const route=`repos/${repository}/releases/assets/${asset.id}`;
      return {feed:JSON.parse(await run(['api',route,'-H','Accept: application/octet-stream'])),feedURL:`https://api.github.com/${route}`};
    },
    authorize:async()=>{
      await exec('/usr/bin/codesign',['--verify','--deep','--strict',appPath],{timeout:15000,maxBuffer:32768});
      const identity=await exec('/usr/bin/codesign',['-dv','--verbose=4',appPath],{timeout:15000,maxBuffer:32768});
      if(!identity.stderr.includes('Authority=Developer ID Application:'))throw Error('Developer ID signature is required.');
      const token=(await run(['auth','token','--hostname','github.com'])).trim();if(!token||/[\r\n]/.test(token))throw Error('Private release authorization unavailable.');
      return {Authorization:`Bearer ${token}`,Accept:'application/octet-stream','X-GitHub-Api-Version':'2022-11-28'};
    },
  };
}
