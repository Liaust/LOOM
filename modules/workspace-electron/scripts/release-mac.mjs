import {execFileSync,spawnSync} from 'node:child_process';
import {extractFile} from '@electron/asar';
import {mkdir,stat,writeFile} from 'node:fs/promises';
import {createReadStream} from 'node:fs';
import {createHash} from 'node:crypto';
import path from 'node:path';

// Produces a code-only artifact, never publishes or changes repository visibility.
const application=process.argv[2];
if(process.platform!=='darwin'||!application?.endsWith('.app'))throw Error('Supply a signed/notarized .app on the Mac.');
const app=path.resolve(application),archive=path.join(app,'Contents/Resources/app.asar');
const build=JSON.parse(extractFile(archive,'build.json').toString());
const manifest=JSON.parse(extractFile(archive,'package.json').toString());
if(build.kind!=='signed-notarized'||build.dirty||!/^[a-f0-9]{40}$/.test(build.source)||!/^\d+\.\d+\.\d+$/.test(manifest.version))throw Error('Only an audited signed release can produce an update artifact.');
execFileSync('/usr/bin/codesign',['--verify','--deep','--strict',app],{stdio:'inherit'});
const signature=spawnSync('/usr/bin/codesign',['-dv','--verbose=4',app],{encoding:'utf8'});
if(signature.status!==0||!signature.stderr.includes('Authority=Developer ID Application:'))throw Error('A Developer ID Application signature is required.');
execFileSync('/usr/bin/xcrun',['stapler','validate',app],{stdio:'inherit'});
const output=path.join(path.dirname(app),'workspace-release');await mkdir(output,{recursive:true,mode:0o700});
const name=`LOOM-Workspace-${manifest.version}-darwin-${process.arch}.zip`,zip=path.join(output,name);
try{await stat(zip);throw Error('The immutable update artifact already exists.');}catch(error){if(error.code!=='ENOENT')throw error;}
execFileSync('/usr/bin/ditto',['-c','-k','--sequesterRsrc','--keepParent',app,zip],{stdio:'inherit'});
const hash=createHash('sha256');for await(const chunk of createReadStream(zip))hash.update(chunk);
const metadata={component:'loom-workspace',channel:'stable',platform:'darwin',arch:process.arch,version:manifest.version,source:build.source,filename:name,size:(await stat(zip)).size,sha256:hash.digest('hex')};
await writeFile(path.join(output,'artifact-manifest.json'),JSON.stringify(metadata,null,2),{flag:'wx',mode:0o600});
console.log(JSON.stringify({output,metadata,next:'Apple-gated private publication and actual two-version update acceptance remain separate. No publication was performed.'},null,2));
