import test from 'node:test';
import assert from 'node:assert/strict';
import {themeOptions,defaultTheme,themeKey,themeID,loadTheme,saveTheme,applyTheme} from '../renderer/themes.js';

test('workspace theme preferences are allowlisted, independent and restart-safe',()=>{
  const data=new Map([['syntax-theme','contrast']]);const storage={getItem:key=>data.get(key),setItem:(key,value)=>data.set(key,value)};
  assert.equal(loadTheme(storage),defaultTheme);assert.equal(themeOptions.length,2);
  assert.equal(saveTheme('coffee-cream',storage),true);assert.equal(loadTheme(storage),'coffee-cream');assert.equal(data.get('syntax-theme'),'contrast');
  assert.throws(()=>saveTheme('untrusted-theme',storage),/Unknown/);assert.equal(data.get(themeKey),'coffee-cream');
  assert.equal(themeID('unexpected'),defaultTheme);data.set(themeKey,'broken');assert.equal(loadTheme(storage),defaultTheme);
});
test('unavailable preference storage leaves theme choice usable for the session',()=>{
  const storage={getItem(){throw Error('unavailable');},setItem(){throw Error('full');}};
  assert.equal(loadTheme(storage),defaultTheme);assert.equal(saveTheme('coffee-cream',storage),false);
});
test('live theme activation changes only palette identity and sends one local event',()=>{
  const events=[];const document={documentElement:{dataset:{}},defaultView:{CustomEvent,dispatchEvent:e=>events.push(e)}};
  assert.equal(applyTheme(document,'coffee-cream'),'coffee-cream');assert.deepEqual(document.documentElement.dataset,{theme:'coffee-cream'});
  assert.equal(events.length,1);assert.equal(events[0].type,'workspace-theme-changed');assert.deepEqual(events[0].detail,{id:'coffee-cream'});
});
