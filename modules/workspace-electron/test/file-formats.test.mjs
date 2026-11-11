import test from 'node:test';
import assert from 'node:assert/strict';
import { isEditableText, isMarkdown, defaultFileName, editorLanguage } from '../src/file-formats.mjs';
import { DocumentTabs } from '../renderer/tabs.js';

test('new filenames default to Markdown without replacing an explicit extension', () => {
  assert.equal(defaultFileName('Untitled'), 'Untitled.md');
  assert.equal(defaultFileName('  Meeting notes  '), 'Meeting notes.md');
  assert.equal(defaultFileName('folder.with.dots/note'), 'folder.with.dots/note.md');
  for (const name of ['code.py', 'source.TSX', 'data.json', 'note.txt', 'board.canvas', 'table.base', 'photo.png', 'file.unknown', '.env', '.gitignore', 'Makefile', 'Dockerfile']) {
    assert.equal(defaultFileName(name), name);
  }
  assert.equal(defaultFileName(''), '');
});

test('common source, text and configuration files are editable, case-insensitively', () => {
  for (const name of ['note.md', 'NOTE.MARKDOWN', 'note.mdown', 'text.txt', 'paper.rst', 'guide.adoc', 'table.csv', 'source.go', 'app.js', 'app.cjs', 'app.tsx', 'app.py', 'app.rs', 'config.yaml', 'config.toml', 'config.jsonc', 'view.html', 'style.css', 'schema.sql', 'shell.sh', 'board.canvas', 'table.base', 'src/Makefile', 'Dockerfile', '.env', '.env.production.local', '.gitignore', '.editorconfig', '.zshrc']) {
    assert.equal(isEditableText(name), true, name);
  }
});

test('binary and unknown extensions are not admitted as editable text', () => {
  for (const name of ['photo.png', 'photo.JPG', 'clip.mp4', 'audio.mp3', 'font.woff2', 'archive.zip', 'report.pdf', 'data.sqlite', 'data.db', 'app.exe', 'object.o', 'book.docx', 'table.xlsx', 'deck.pptx', 'file.bin', 'note.md.png', '.env.png', 'file.unknown', 'folder.md/file', 'README', '']) {
    assert.equal(isEditableText(name), false, name);
  }
});

test('Markdown detection and editor language follow the final filename, not its parent', () => {
  for (const name of ['note.md', 'notes/NOTE.MARKDOWN', 'note.mdown', 'note.mkd']) assert.equal(isMarkdown(name), true, name);
  for (const name of ['note.txt', 'board.canvas', 'view.mdx', 'note.md.js', 'folder.md/note.py']) assert.equal(isMarkdown(name), false, name);
  for (const [name, language] of [['notes.md', 'markdown'], ['main.py', 'python'], ['app.TSX', 'typescript'], ['board.canvas', 'json'], ['table.base', 'yaml'], ['Makefile', 'makefile'], ['Dockerfile', 'dockerfile'], ['.env.local', 'ini'], ['notes.txt', 'plaintext'], ['photo.png', 'plaintext']]) {
    assert.equal(editorLanguage(name), language, name);
  }
});

test('accepted format-changing renames keep the same draft, selection and dirty state', () => {
  const tabs = new DocumentTabs();
  const draft = { root: 'box', path: 'note.md', entry: { name: 'note.md' }, text: 'unsaved text', selection: [2, 7, 40, 'backward', 0], dirty: true, editing: true };
  tabs.open(draft);
  for (const [from, to, language] of [['note.md', 'script.py', 'python'], ['script.py', 'note.md', 'markdown']]) {
    assert.equal(tabs.move('box', from, to, `box/${to}`), true);
    assert.equal(tabs.get('box', to), draft);
    assert.equal(editorLanguage(draft.path), language);
    assert.equal(draft.text, 'unsaved text');
    assert.deepEqual(draft.selection, [2, 7, 40, 'backward', 0]);
    assert.equal(draft.dirty, true);
    assert.equal(draft.editing, true);
  }
});
