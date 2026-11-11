import test from 'node:test';
import assert from 'node:assert/strict';
import { EditorState } from '@codemirror/state';
import { markdown } from '@codemirror/lang-markdown';
import { syntaxTree } from '@codemirror/language';
import { GFM } from '@lezer/markdown';
import { markdownPresentation } from '../renderer/markdown-presentation.js';
import {richSpans} from '../renderer/rich-markdown.js';

function spans(text, cursor = 0, focused = false) {
  const state = EditorState.create({ doc: text, selection: { anchor: cursor }, extensions: [markdown({ extensions: [GFM] })] });
  const result = markdownPresentation(state, syntaxTree(state), 0, text.length, focused);
  assert.equal(state.doc.toString(), text); return result;
}
test('Markdown presentation formats headings/emphasis/lists without changing source bytes', () => {
  const result = spans('# Heading\n\n**bold** and *emphasis*\n\n- one\n- [ ] task\n\n> quote\n');
  for (const className of ['lp-heading lp-h1', 'lp-strong', 'lp-emphasis', 'lp-quote']) assert.ok(result.some(span => span.className === className), className);
  assert.ok(result.some(span => span.kind === 'bullet')); assert.ok(result.some(span => span.kind === 'task'));
  assert.ok(result.some(span => span.kind === 'hide' && span.from === 0));
});
test('editing a Markdown construct reveals its syntax but keeps its formatting', () => {
  const text = '# Heading\n\n**bold**\n';
  const result = spans(text, text.indexOf('bold') + 1, true);
  assert.ok(result.some(span => span.className === 'lp-strong'));
  assert.equal(result.some(span => span.kind === 'hide' && span.from === text.indexOf('**')), false);
  assert.ok(result.some(span => span.kind === 'hide' && span.from === 0));
});
test('links navigate by their actual target; external images are not executable HTML', () => {
  const result = spans('[label](folder/note.md) [[another|Alias]] ![image](https://example.com/pixel.png)\n<script>evil()</script>');
  assert.ok(result.some(span => span.reference === 'folder/note.md'));
  assert.ok(result.some(span => span.reference === 'another'));
  assert.ok(result.some(span => span.kind === 'image' && span.reference === 'https://example.com/pixel.png'));
  assert.equal(result.some(span => span.kind === 'html'), false);
});
test('frontmatter, code and literal wiki syntax are retained rather than reinterpreted', () => {
  const result = spans('---\nkind: note\n---\n\n# Title\n\n`[[literal]]`\n\n```js\n[[also literal]]\n```');
  assert.ok(result.some(span => span.className === 'lp-metadata')); assert.ok(result.some(span => span.className === 'lp-code-block'));
  assert.equal(result.some(span => span.reference === 'literal' || span.reference === 'also literal'), false);
});
test('nested emphasis has an explicit combined colour in both nesting orders without changing bytes',()=>{
  for(const source of ['***both***','**strong *both* strong**','*emphasis **both** emphasis*','___both___'])assert.ok(spans(source).some(span=>span.className==='lp-strong-emphasis'),source);
  assert.equal(spans('`***literal***`').some(span=>span.className==='lp-strong-emphasis'),false);
});
test('section rules are parsed separately from YAML, Setext headings and code',()=>{
  const text='---\ntype: note\n---\n\nparagraph\n\n---\n\nHeading\n---\n\n```md\n---\n```\n\n`---`\n';
  const state=EditorState.create({doc:text,extensions:[markdown({extensions:[GFM]})]});
  const rules=richSpans(state,syntaxTree(state)).filter(span=>span.kind==='divider');assert.equal(rules.length,1);assert.equal(state.doc.sliceString(rules[0].from,rules[0].to),'---');assert.equal(state.doc.toString(),text);
  const unclosed=EditorState.create({doc:'---\ntitle: not closed',extensions:[markdown()]});assert.equal(richSpans(unclosed,syntaxTree(unclosed)).length,0);
});
