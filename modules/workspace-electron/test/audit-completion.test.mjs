import test from 'node:test';
import assert from 'node:assert/strict';
import {EditorState} from '@codemirror/state';
import {markdown} from '@codemirror/lang-markdown';
import {GFM} from '@lezer/markdown';
import {syntaxTree} from '@codemirror/language';
import {richSpans} from '../renderer/rich-markdown.js';
import {scheduleTiming} from '../src/schedule-timing.mjs';
import {Operations,evidenceText,resourceDescription} from '../src/operations.mjs';

test('non-runtime resources do not acquire misleading unknown health labels',()=>{
  assert.equal(resourceDescription({binding:{kind:'knowledge'},readiness:{applied:{state:'unknown'},healthy:{state:'unknown'}}}),'knowledge');
  assert.match(resourceDescription({binding:{kind:'service'}}),/No runtime health observation/);
  assert.match(resourceDescription({binding:{kind:'service'},readiness:{healthy:{state:'healthy'}}}),/Health: healthy/);
});

test('rich tables and math preserve exact source and exclude code/metadata',()=>{
  const doc='---\ntitle: "$not math$"\n---\n\n| A | B |\n| - | - |\n| x | y |\n\nInline $E=mc^2$.\n\n$$\n\\int_0^1 x^2 dx\n$$\n\n`$literal$`\n\n```sh\necho "$HOME"\n```\n\\$escaped and $5 for lunch.\n';
  const state=EditorState.create({doc,extensions:[markdown({extensions:[GFM]})]}),spans=richSpans(state,syntaxTree(state));
  assert.deepEqual(spans.map(s=>s.kind),['table','math','math']);assert.equal(spans[1].source,'E=mc^2');assert.equal(spans[2].block,true);assert.equal(state.doc.toString(),doc);
});
test('friendly timing generates explicit owner expressions and unambiguous once instants',()=>{
  assert.deepEqual(scheduleTiming({cadence:'daily',time:'09:15',timezone:'Europe/Amsterdam'}),{kind:'cron',expression:'15 9 * * *',timezone:'Europe/Amsterdam'});
  assert.equal(scheduleTiming({cadence:'weekly',time:'19:05',weekday:'1'}).expression,'5 19 * * 1');
  assert.equal(scheduleTiming({cadence:'once',at:'2099-01-01T10:00:00+01:00'}).expression,'2099-01-01T09:00:00.000Z');
  for(const input of [{cadence:'daily',time:'25:00'},{cadence:'weekly',time:'09:00',weekday:'9'},{cadence:'once',at:'2020-01-01'}])assert.throws(()=>scheduleTiming(input));
});
test('job evidence is opt-in, bounded and redacts credential fields',async()=>{
  const remote={api:async()=>({data:{job:{status:'failed',failure_code:'codex_needs_input'},outputs:[{output_key:'question',value_json:{question:'Which chapter?',token:'do-not-show'}},{output_key:'thread_id',value_json:'00000000-0000-4000-8000-000000000001'}],logs:[{tail_text:'retained tail'}],artifacts:[]}})};
  const op=new Operations(remote);assert.equal((await op.inspect({kind:'job',ref:'job_one'})).sections,undefined);
  const view=await op.inspect({kind:'job',ref:'job_one',evidence:true});assert.match(view.sections[0].text,/Which chapter/);assert.equal(view.links.length,1);assert.doesNotMatch(JSON.stringify(view),/do-not-show/);assert.match(evidenceText({api_key:'secret'}),/Redacted/);
});
test('native schedule actions keep exact owner/profile and create paused through one replay token',async()=>{
  const calls=[],op=new Operations({async api(route,query,options){calls.push({route,...options});return{data:{job:{id:'abcdef123456',paused:true}}};}});
  const input={kind:'schedule-create',owner:'hermes',profile:'mina',host:'main',name:'Review',prompt:'Read project status',expression:'0 9 * * *'};
  await op.action(input);assert.equal(calls[0].body.operation,'workspace_create');assert.equal(calls[0].body.profile,'mina');assert.deepEqual(Object.keys(calls[0].body.spec).sort(),['name','prompt','schedule']);
  await assert.rejects(op.action({...input,profile:'other'}),/exact registered native profile/);
});
