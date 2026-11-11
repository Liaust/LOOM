import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {EditorState} from '@codemirror/state';
import {markdown} from '@codemirror/lang-markdown';
import {CompletionContext} from '@codemirror/autocomplete';
import {fileTitle,titleFilename} from '../renderer/document-title.js';
import {renameProperty,frontmatter} from '../renderer/frontmatter.js';
import {completionRequest} from '../renderer/note-completions.js';
import {languageSupport,syntaxThemes,syntaxTheme} from '../renderer/syntax-themes.js';
import {noteMetadata} from '../src/note-metadata.mjs';
import {NoteGraph} from '../src/note-graph.mjs';

test('title preparation changes no file, preserves extensions and rejects paths',()=>{
  assert.equal(fileTitle('Notes/Idea v1.2.md'),'Idea v1.2');assert.equal(titleFilename('Notes/Idea.md','Idea'),null);assert.equal(titleFilename('Notes/Idea.md','New v1.2'),'New v1.2.md');assert.equal(titleFilename('src/test.py','worker'),'worker.py');assert.equal(titleFilename('src/test.py','worker.cpp'),'worker.cpp');for(const value of ['','../escape','nested/path','x\nname'])assert.throws(()=>titleFilename('note.md',value));
});
test('property-key rename retains the value/comment/body and refuses collisions',()=>{
  const text='---\nstatus: working # retained\ntags: [one]\n---\n\nExact body\n';const change=renameProperty(text,'status','stage');assert.match(change.text,/stage: working # retained/);assert.equal(frontmatter(change.text).body,'\nExact body\n');assert.throws(()=>renameProperty(text,'status','tags'));
});
test('real language supports load for requested languages and themes are independent',async()=>{
  for(const file of ['a.yaml','a.base','a.py','a.java','a.c','a.cpp','a.cs','a.ts'])assert.ok((await languageSupport(file)).language,file);
  assert.equal(syntaxThemes.length,2);assert.ok(syntaxTheme('one-dark'));assert.ok(syntaxTheme('contrast'));
});
test('tag and wiki metadata ignore code, escaped syntax, headings and URL fragments',()=>{
  const m=noteMetadata('---\naliases: [Alias]\ntags: [frontmatter]\nrelated: ["[[Previous]]"]\n---\n# Heading\n\n[[Target|Label]] #physics/qm [Paper](paper.pdf)\n`[[literal]] #literal`\n\n```md\n[[not-link]] #not-tag\n```\n\\[[escaped]] \\#escaped https://example.com/#fragment\n');
  assert.deepEqual(m.aliases,['Alias']);assert.deepEqual(m.tags,['frontmatter','physics/qm']);assert.deepEqual(new Set(m.references),new Set(['Target','Previous','paper.pdf']));assert.equal(m.tagSpans.length,1);
});
test('completion begins at tag/wiki target and never inside code or metadata',()=>{
  const request=text=>{const state=EditorState.create({doc:text,extensions:[markdown()]});return completionRequest(new CompletionContext(state,text.length,false));};
  assert.deepEqual(request('A [[Tar'),{kind:'link',from:4,query:'Tar'});assert.deepEqual(request('A #phys'),{kind:'tag',from:3,query:'phys'});
  for(const text of ['`[[literal`','```\n[[literal','---\ntags: [phy','[[Target|label','[[Target#heading'])assert.equal(request(text),null,text);
});
test('offline graph uses local aliases, excludes removed pending links, and retains Main cache separately',async()=>{
  const db=new DatabaseSync(':memory:'),a={root:'box',path:'Notes/A.md',text:'[[B]] #local',pending:false},b={root:'box',path:'Notes/B.md',text:'---\naliases: [Bee]\n---\nB'};
  const notes={db,records:new Map([['a',a],['b',b]])};let online=true;
  const remote={async json(endpoint,input){if(input.action==='complete')return{data:{items:[{root:'box',path:b.path,name:'B',kind:'link'}],coverage:'known'}};if(input.action==='resolve')return{data:{choices:[{root:'box',path:b.path}]}};return{data:{forward:[],backlinks:[{root:'box',path:a.path,name:'A'}],coverage:'known Main catalog'}};}};
  const graph=new NoteGraph(notes,remote,{online:()=>online}).init();const first=await graph.links({root:'box',path:b.path});assert.equal(first.backlinks.length,1);assert.equal(first.origin,'main');online=false;
  assert.equal((await graph.resolve({root:'box',from:a.path,reference:'[[Bee]]'})).path,b.path);
  a.text='Removed the link';a.pending=true;const second=await graph.links({root:'box',path:b.path});assert.equal(second.backlinks.length,0);assert.equal(second.origin,'cached');assert.match(second.qualification,/not current whole-Main/);
  assert.equal((await graph.complete({root:'box',kind:'tag',query:'local'})).items.length,0);assert.equal(db.prepare('SELECT count(*) AS n FROM navigation_cache').get().n,1);db.close();
});
