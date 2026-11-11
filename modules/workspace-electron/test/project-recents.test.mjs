import test from 'node:test';
import assert from 'node:assert/strict';
import {recentProjects,rememberProject} from '../renderer/project-recents.js';

test('legacy recents stay Personal, scoped recents never bleed between Boxes',()=>{
  const values=new Map([['loom.recentProjects',JSON.stringify([{id:'p',name:'Personal project'}])]]);
  const storage={getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value)};
  assert.equal(recentProjects(storage,'box')[0].id,'p');
  assert.deepEqual(recentProjects(storage,'box:company'),[]);
  rememberProject(storage,'box:company',{id:'c',name:'Company project'});
  assert.deepEqual(recentProjects(storage,'box:company'),[{id:'c',name:'Company project'}]);
  assert.equal(recentProjects(storage,'box')[0].id,'p');
  rememberProject(storage,'box',{id:'p2',name:'Second personal project'});
  assert.deepEqual(recentProjects(storage,'box').map(item=>item.id),['p2','p']);
  assert.equal(recentProjects(storage,'box:company')[0].id,'c');
});
