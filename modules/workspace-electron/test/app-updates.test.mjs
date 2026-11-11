import test from 'node:test';
import assert from 'node:assert/strict';
import {EventEmitter} from 'node:events';
import {AppUpdates,newerVersion,validateUpdateFeed} from '../src/app-updates.mjs';
const repository='example-operator/loom-releases';
const feed={component:'loom-workspace',channel:'stable',platform:'darwin',arch:'arm64',version:'0.2.0',source:'a'.repeat(40),sha256:'b'.repeat(64),size:200,notes:'Changes',url:`https://api.github.com/repos/${repository}/releases/assets/123`};
test('update discovery validates identity, immutable asset, architecture and monotonic version',()=>{
 assert.ok(newerVersion('0.10.0','0.9.0'));assert.ok(!newerVersion('0.1.0','0.2.0'));
 assert.equal(validateUpdateFeed(feed,{version:'0.1.0',arch:'arm64',repository}).version,'0.2.0');
 for(const changed of [{channel:'server'},{arch:'x64'},{version:'0.0.1'},{url:'https://evil.example/app.zip'},{url:'https://api.github.com/repos/other/project/releases/assets/123'},{url:feed.url+'?token=untrusted'},{sha256:'wrong'},{size:Infinity}])assert.throws(()=>validateUpdateFeed({...feed,...changed},{version:'0.1.0',arch:'arm64',repository}));
});
test('development checks do not request a feed or native download',async()=>{
 let reads=0;const native=new EventEmitter();native.checkForUpdates=()=>{throw Error('not permitted');};
 const updater=new AppUpdates({native,build:{kind:'local-development'},version:'0.1.0',platform:'darwin',load:async()=>{reads++;}});
 assert.equal((await updater.check()).state,'setup');assert.equal(reads,0);await assert.rejects(updater.download());
});
test('manual discovery never downloads; install waits for durable preparation',async()=>{
 let downloads=0,installed=0,prepared=false;const native=new EventEmitter();native.setFeedURL=()=>{};native.checkForUpdates=()=>downloads++;native.quitAndInstall=()=>{assert.ok(prepared);installed++;};
 const updater=new AppUpdates({native,build:{kind:'signed-notarized'},version:'0.1.0',arch:'arm64',platform:'darwin',repository,load:async()=>({feed,feedURL:feed.url}),authorize:async()=>({}),prepareInstall:async()=>{prepared=true;}});
 assert.equal((await updater.check()).state,'available');assert.equal(downloads,0);await updater.download();assert.equal(downloads,1);await assert.rejects(updater.download());native.emit('update-downloaded');await updater.install();assert.equal(installed,1);
});
test('signed source builds without an operator channel never request a feed',async()=>{
 let reads=0;const native=new EventEmitter();
 const updater=new AppUpdates({native,build:{kind:'signed-notarized'},version:'0.1.0',platform:'darwin',repository:'',load:async()=>{reads++;}});
 assert.equal((await updater.check()).state,'setup');assert.equal(reads,0);
});
test('failed local preparation keeps the downloaded update staged without restarting',async()=>{
 let installed=0;const native=new EventEmitter();native.quitAndInstall=()=>installed++;
 const updater=new AppUpdates({native,build:{kind:'signed-notarized'},version:'0.1.0',prepareInstall:async()=>{throw Error('draft persistence failed');}});
 native.emit('update-downloaded');await assert.rejects(updater.install(),/Restart held/);assert.equal(installed,0);assert.equal(updater.status().state,'ready');
});
