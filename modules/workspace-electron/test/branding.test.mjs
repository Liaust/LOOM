import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';

test('Loom display name preserves the existing private data and update identities',async()=>{
  const read=relative=>readFile(new URL(relative,import.meta.url),'utf8');
  const main=await read('../src/main.mjs'),pack=await read('../scripts/package-mac.mjs');
  assert.match(main,/app\.setName\('Loom'\)/);
  assert.match(main,/app\.setPath\('userData',path\.join\(app\.getPath\('appData'\),'LOOM Files'\)\)/);
  assert.match(pack,/name:'Loom',icon/);
  assert.match(pack,/appBundleId:'com\.liaust\.loom\.workspace'/);
  assert.match(await read('../renderer/index.html'),/<title>Loom<\/title>/);
});

test('native appearance variants use the supplied spool without runtime icon rewrites',async()=>{
  const icon=JSON.parse(await readFile(new URL('../assets/Loom.icon/icon.json',import.meta.url),'utf8'));
  assert.equal(icon['fill-specializations'][0].value.solid,'srgb:1.00000,1.00000,1.00000,1.00000');
  assert.equal(icon['fill-specializations'][1].appearance,'dark');
  assert.equal(icon.groups[0].layers[0]['image-name'],'loom-spool.svg');
  assert.equal(icon.groups[0].layers[0].glass,false);
  assert.equal(icon.groups[0].layers[0]['position-specializations'][0].value.scale,0.88);
  const svg=await readFile(new URL('../assets/Loom.icon/Assets/loom-spool.svg',import.meta.url),'utf8');
  assert.match(svg,/<svg/);assert.doesNotMatch(svg,/<script|<foreignObject|<image/);
});
