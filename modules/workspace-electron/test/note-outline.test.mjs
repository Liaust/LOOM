import test from 'node:test';
import assert from 'node:assert/strict';
import { noteOutline } from '../renderer/note-outline.js';

test('outline includes ATX and setext headings with exact source positions', () => {
  const source = '# One\n\n## Two ##\n\nThree\n=====\n\nFour\n----\n';
  const result = noteOutline(source);
  assert.deepEqual(result.map(({text,level,line}) => ({text,level,line})), [
    {text:'One',level:1,line:1}, {text:'Two',level:2,line:3}, {text:'Three',level:1,line:5}, {text:'Four',level:2,line:8},
  ]);
  assert.equal(source.slice(result[1].from, result[1].to), '## Two ##');
  assert.equal(source.slice(result[2].from, result[2].to), 'Three\n=====');
});

test('outline ignores YAML frontmatter with BOM/CRLF, including invalid YAML', () => {
  for (const close of ['---', '...']) {
    const prefix = '\uFEFF---\r\ntitle: [\r\n# Not a heading\r\n'+close+'\r\n\r\n';
    assert.deepEqual(noteOutline(prefix+'# Actual\r\n'), [{text:'Actual',level:1,line:6,from:prefix.length,to:prefix.length+8}]);
  }
  assert.deepEqual(noteOutline('---\ntitle: unclosed\n# Still YAML'), []);
  assert.equal(noteOutline('\uFEFF# Actual')[0].from, 1);
});

test('outline ignores fenced/indented code, including longer and unclosed fences', () => {
  const source = '# Visible\n\n```md\n# Hidden\n```\n\n~~~~md\n## Hidden too\n~~~\n# Still hidden\n~~~~\n\n    # Indented\n\n## Visible two\n\n```\n# Unclosed';
  assert.deepEqual(noteOutline(source).map(item => item.text), ['Visible', 'Visible two']);
});

test('outline uses parser boundaries for quotes/lists, not hash-shaped prose', () => {
  const source = '#no-space\n\n####### too many\n\n\\# escaped\n\n> ## Quoted\n\n- ### Nested\n\ntext # not a heading\n';
  assert.deepEqual(noteOutline(source).map(({text,level}) => ({text,level})), [{text:'Quoted',level:2},{text:'Nested',level:3}]);
});

test('outline renders inline labels as text without URLs or Markdown delimiters', () => {
  const result = noteOutline('# **Bold** *soft* `code` [Label](target.md "Title") [[path|Alias]] <b>safe</b>\n\n## Duplicate\n\n## Duplicate\n');
  assert.equal(result[0].text, 'Bold soft code Label Alias safe');
  assert.equal(result[1].text, result[2].text); assert.notEqual(result[1].from, result[2].from);
  assert.deepEqual(noteOutline(''), []); assert.deepEqual(noteOutline(null), []);
});
