import test from 'node:test';
import assert from 'node:assert/strict';
import { TreeSelection, treeEntryKey } from '../renderer/tree-selection.js';
import { FileTree } from '../renderer/file-tree.js';

const entry = (path, extra = {}) => ({ root: 'box', path, directory: false, ...extra });
const entries = ['a', 'b', 'c', 'd'].map(path => entry(path));

test('modifier toggles and anchored ranges follow visible order, including across roots', () => {
  const selection = new TreeSelection(), visible = [...entries, entry('e', { root: 'mac' })];
  selection.sync(visible);
  selection.select(visible[1]);
  selection.select(visible[3], { shiftKey: true });
  assert.deepEqual(selection.entries(), visible.slice(1, 4));
  selection.select(visible[2], { ctrlKey: true });
  assert.deepEqual(selection.entries(), [visible[1], visible[3]]);
  selection.select(visible[4], { metaKey: true, shiftKey: true });
  assert.deepEqual(selection.entries(), visible.slice(1));
  selection.select(visible[0], { shiftKey: true });
  assert.deepEqual(selection.entries(), visible.slice(0, 3));
});

test('right-click preserves selected groups but selects an unselected target alone', () => {
  const selection = new TreeSelection();selection.sync(entries);
  selection.select(entries[0]);selection.select(entries[1], { metaKey: true });
  selection.select(entries[0], { preserve: true });
  assert.deepEqual(selection.entries(), entries.slice(0, 2));
  selection.select(entries[3], { preserve: true });
  assert.deepEqual(selection.entries(), [entries[3]]);
});

test('archive member identities stay distinct and root headers are not command entries', () => {
  const selection = new TreeSelection();
  const root = entry('', { directory: true, isRoot: true });
  const first = entry('papers.zip', { member: { id: 'one', name: 'one.pdf' } });
  const second = entry('papers.zip', { member: { id: 'two', name: 'two.pdf' } });
  selection.sync([root, first, second]);selection.select(root);selection.select(second, { shiftKey: true });
  assert.deepEqual(selection.entries(), [first, second]);
  assert.notEqual(treeEntryKey(first), treeEntryKey(second));
  assert.equal(treeEntryKey(first), treeEntryKey({ ...first, member: { name: 'new.pdf', id: 'one' } }));
});

test('refresh prunes hidden selections and stale anchors without selecting unseen paths', () => {
  const selection = new TreeSelection();selection.sync(entries);
  selection.select(entries[1]);selection.select(entries[3], { shiftKey: true });
  selection.sync([entries[0], entries[2]]);
  assert.deepEqual(selection.entries(), [entries[2]]);
  selection.select(entries[0], { shiftKey: true });
  assert.deepEqual(selection.entries(), [entries[0]]);
});

test('document navigation follows external selection without collapsing a tree-initiated open', () => {
  const selection = new TreeSelection();selection.sync(entries, entries[0]);
  selection.select(entries[1], { metaKey: true });selection.opening(entries[1]);
  selection.sync(entries, entries[1]);assert.deepEqual(selection.entries(), entries.slice(0, 2));
  selection.sync(entries, entries[3]);assert.deepEqual(selection.entries(), [entries[3]]);
  selection.sync([], entries[3]);assert.deepEqual(selection.entries(), []);
  selection.sync(entries, entries[3]);assert.deepEqual(selection.entries(), [entries[3]]);
  selection.select(entries[3], { ctrlKey: true });selection.sync(entries, entries[3]);
  assert.deepEqual(selection.entries(), []);
});

// Only the element operations used by FileTree; no browser or app process needed.
function element(tag) {
  const attributes = new Map();
  const node = { tagName: tag.toUpperCase(), dataset: {}, style: {}, children: [], scrollTop: 0, className: '', tabIndex: -1,
    append(...children) { for (const child of children) { child.parentElement = this;this.children.push(child); } },
    replaceChildren(...children) { this.children = [];this.append(...children); },
    setAttribute(name, value) { attributes.set(name, String(value));if (name === 'tabindex') this.tabIndex = Number(value); },
    getAttribute(name) { return attributes.get(name) ?? null; },
    addEventListener(name, callback) { this['listener_' + name] = callback; },
    contains(target) { return this === target || this.children.some(child => child.contains(target)); },
    matches(selector) {
      if (selector === '[role=treeitem]') return this.getAttribute('role') === 'treeitem';
      if (selector.startsWith('.')) return this.className.split(' ').includes(selector.slice(1));
      return selector === 'input,textarea,select,[contenteditable="true"]' && ['INPUT', 'TEXTAREA', 'SELECT'].includes(this.tagName);
    },
    closest(selector) { return this.matches(selector) ? this : this.parentElement?.closest(selector); },
    querySelectorAll(selector) { return this.children.flatMap(child => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]); },
    querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
    focus() { document.activeElement = this; }, scrollIntoView() {},
    getBoundingClientRect() { return { left: 0, bottom: 30 }; },
    remove() { if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(child => child !== this); },
  };
  node.classList = {
    toggle(name, active) { const values = new Set(node.className.split(' ').filter(Boolean));if (active) values.add(name);else values.delete(name);node.className = [...values].join(' '); },
    add(name) { this.toggle(name, true); }, remove(name) { this.toggle(name, false); },
    contains(name) { return node.className.split(' ').includes(name); },
  };
  return node;
}

function fixture(t) {
  const previous = globalThis.document;
  globalThis.document = { createElement: element, activeElement: element('body') };
  t.after(() => { if (previous === undefined) delete globalThis.document;else globalThis.document = previous; });
  const container = element('div'), opens = [], folders = [], toggles = [], commands = [], menus = [];
  let current;
  const tree = new FileTree(container, {
    loader: async () => { throw Error('Unexpected directory read'); },
    roots: () => [{ ref: 'box', label: 'Box' }], selected: () => current,
    openFile: (...args) => opens.push(args), selectFolder: (...args) => folders.push(args),
    command: (...args) => commands.push(args), contextMenu: (...args) => menus.push([args, tree.selectedEntries()]),
    icon: () => element('span'), icons() {},
  });
  tree.model.remember('box', '', { entries: [
    { name: 'Notes', relative_path: 'Notes', kind: 'directory' },
    ...['a', 'b', 'c'].map(name => ({ name, relative_path: name, kind: 'file' })),
  ] });
  tree.model.expanded.add(tree.model.key('box', ''));
  tree.render();tree.toggle = (...args) => toggles.push(args);
  const row = path => container.querySelectorAll('[role=treeitem]').find(row => row.dataset.path === path);
  return { tree, container, row, opens, folders, toggles, commands, menus, current: value => { current = value; } };
}

function event(target, extra = {}) {
  return { target, key: '', metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, button: 0,
    preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.stopped = true; }, ...extra };
}

test('file click intents and modifier folder selection preserve ordinary navigation', t => {
  const f = fixture(t);
  f.row('a').onclick(event(f.row('a')));
  assert.deepEqual(f.opens.pop(), ['a', 'box', { member: undefined }]);
  f.row('b').onclick(event(f.row('b'), { metaKey: true }));
  assert.deepEqual(f.opens.pop(), ['b', 'box', { member: undefined, newTab: true, background: false }]);
  f.current(entry('b'));f.tree.render();
  assert.deepEqual(f.tree.selectedEntries(), [entry('a'), entry('b')]);
  for (const key of ['metaKey', 'ctrlKey', 'shiftKey']) f.row('Notes').onclick(event(f.row('Notes'), { [key]: true }));
  assert.deepEqual(f.folders, []);assert.deepEqual(f.toggles, []);
  f.row('Notes').onclick(event(f.row('Notes')));
  assert.deepEqual(f.folders, [['box', 'Notes']]);assert.deepEqual(f.toggles, [['box', 'Notes']]);
  assert.deepEqual(f.tree.selectedEntries(), [entry('Notes', { directory: true })]);
  assert.equal(f.container.getAttribute('aria-multiselectable'), 'true');
  assert.equal(f.row('Notes').getAttribute('aria-selected'), 'true');
  assert.equal(f.row('Notes').classList.contains('is-selected'), true);
  assert.equal(f.row('a').getAttribute('aria-selected'), 'false');
  assert.equal(f.row('a').classList.contains('is-selected'), false);
});

test('shift file ranges select without opening, while Cmd/Ctrl-Shift and middle open background tabs', t => {
  const f = fixture(t);
  f.row('a').onclick(event(f.row('a')));f.opens.length = 0;
  f.row('c').onclick(event(f.row('c'), { shiftKey: true }));
  assert.deepEqual(f.tree.selectedEntries(), ['a', 'b', 'c'].map(path => entry(path)));
  assert.deepEqual(f.opens, []);
  for (const modifier of ['metaKey', 'ctrlKey']) {
    f.row('b').onclick(event(f.row('b'), { [modifier]: true, shiftKey: true }));
    assert.deepEqual(f.opens.pop(), ['b', 'box', { member: undefined, newTab: true, background: true }]);
  }
  const before = f.tree.selectedEntries(), middle = event(f.row('a'), { button: 1 });
  f.row('a').onmousedown(middle);assert.equal(middle.defaultPrevented, true);
  f.row('a').onauxclick(middle);
  assert.deepEqual(f.opens.pop(), ['a', 'box', { member: undefined, newTab: true, background: true }]);
  assert.deepEqual(f.tree.selectedEntries(), before);
  f.row('Notes').onauxclick(event(f.row('Notes'), { button: 1 }));
  assert.deepEqual(f.opens, []);assert.deepEqual(f.toggles, []);
});

test('pointer and keyboard context menus observe coherent selection before their callback', t => {
  const f = fixture(t);
  f.row('a').onclick(event(f.row('a')));f.row('b').onclick(event(f.row('b'), { ctrlKey: true }));
  f.row('a').oncontextmenu(event(f.row('a')));
  assert.deepEqual(f.menus.pop()[1], [entry('a'), entry('b')]);
  f.row('c').listener_keydown(event(f.row('c'), { key: 'ContextMenu' }));
  assert.deepEqual(f.menus.pop()[1], [entry('c')]);
  f.row('a').listener_keydown(event(f.row('a'), { key: 'F10', shiftKey: true }));
  assert.deepEqual(f.menus.pop()[1], [entry('a')]);
});

test('tree keyboard commands pass the selection and original event and prevent browser defaults', t => {
  const f = fixture(t), row = f.row('a');row.onclick(event(row));
  for (const [key, modifiers, action] of [
    ['F2', {}, 'rename'], ['c', { metaKey: true }, 'copy'], ['X', { ctrlKey: true }, 'cut'], ['v', { metaKey: true }, 'paste'],
  ]) {
    const keyEvent = event(row, { key, ...modifiers });f.tree.keydown(keyEvent);
    assert.deepEqual(f.commands.pop(), [action, [entry('a')], keyEvent]);
    assert.equal(keyEvent.defaultPrevented, true);assert.equal(keyEvent.stopped, true);
  }
  for (const keyEvent of [
    event(element('button'), { key: 'c', ctrlKey: true }),
    event(row, { key: 'c' }), event(row, { key: 'v', metaKey: true, shiftKey: true }),
    event(row, { key: 'x', ctrlKey: true, altKey: true }), event(row, { key: 'F2', isComposing: true }),
  ]) { f.tree.keydown(keyEvent);assert.equal(keyEvent.defaultPrevented, undefined); }
  const input = element('input');row.append(input);
  const typing = event(input, { key: 'c', ctrlKey: true });f.tree.keydown(typing);
  assert.equal(typing.defaultPrevented, undefined);assert.deepEqual(f.commands, []);
});

test('keyboard range movement does not navigate and Cmd-Enter retains new-tab intent', t => {
  const f = fixture(t);f.row('a').onclick(event(f.row('a')));f.opens.length = 0;
  f.tree.keydown(event(f.row('a'), { key: 'ArrowDown', shiftKey: true }));
  assert.deepEqual(f.tree.selectedEntries(), [entry('a'), entry('b')]);
  f.tree.keydown(event(f.row('b'), { key: 'ArrowDown', ctrlKey: true }));
  assert.deepEqual(f.tree.selectedEntries(), [entry('a'), entry('b')]);
  assert.deepEqual(f.opens, []);
  f.tree.keydown(event(f.row('c'), { key: 'Enter', metaKey: true }));
  assert.deepEqual(f.opens.pop(), ['c', 'box', { member: undefined, newTab: true, background: false }]);
});

test('empty-create blur removes only its naming row and preserves the pending click target', async t => {
  const f = fixture(t);let resolved = 0;
  f.tree.creation = { root: 'box', path: '', name: '', placeholder: 'New file', resolve: result => { assert.equal(result, null);resolved++; } };
  f.tree.render();
  const input = f.container.querySelector('.tree-create-input'), target = f.row('a');
  document.activeElement = target;
  f.tree.render = () => { throw Error('Blur must not rebuild the tree'); };
  input.onblur();await Promise.resolve();
  assert.equal(f.tree.creation, null);assert.equal(resolved, 1);
  assert.equal(f.container.querySelector('.tree-create-input'), null);assert.equal(f.row('a'), target);
  target.onclick(event(target));assert.deepEqual(f.opens.pop(), ['a', 'box', { member: undefined }]);
});
