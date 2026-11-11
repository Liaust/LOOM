import test from 'node:test';
import assert from 'node:assert/strict';
import { marked } from 'marked';
import { Sections } from '../renderer/sections.js';
import { navigationIntent } from '../renderer/navigation-intent.js';
import { resolveProjectLink, projectExternalURL, projectProse, projectProsePreview, projectProsePaths,alternateProjectNavigation } from '../renderer/project-links.js';

const detail = { root: 'box', path: 'Projects/one', facts: { Folder: '/srv/loom/box/Projects/one' } };
const file = (path, extra = {}) => ({ root: 'box', path: `Projects/one/${path}`, ...extra });

test('project-relative links normalize only within the registered project', () => {
  assert.deepEqual(resolveProjectLink('README.md', detail), file('README.md'));
  assert.deepEqual(resolveProjectLink('./.project/STATE.md', detail), file('.project/STATE.md'));
  assert.deepEqual(resolveProjectLink('docs/../README.md', detail), file('README.md'));
  assert.deepEqual(resolveProjectLink('docs/My%20Note.md#A%20heading', detail), file('docs/My Note.md', { heading: 'A heading' }));
  assert.deepEqual(resolveProjectLink('report.pdf#page=4', detail), file('report.pdf', { heading: 'page=4' }));
  assert.deepEqual(resolveProjectLink('./', detail), { root: 'box', path: 'Projects/one', directory: true });
  assert.deepEqual(resolveProjectLink('docs/', detail), file('docs', { directory: true }));
});

test('already-rooted resources and absolute project paths do not duplicate the project prefix', () => {
  for (const rooted of [false, true]) {
    assert.deepEqual(resolveProjectLink('Projects/one/repos/main/README.md', detail, { rooted }), file('repos/main/README.md'));
    assert.deepEqual(resolveProjectLink('/srv/loom/box/Projects/one/repos/main/README.md', detail, { rooted }), file('repos/main/README.md'));
  }
  assert.equal(resolveProjectLink('repos/main/README.md', detail, { rooted: true }), null);
  assert.equal(resolveProjectLink('Projects/two/README.md', detail, { rooted: true }), null);
  assert.equal(resolveProjectLink('Projects/one-other/README.md', detail, { rooted: true }), null);
  assert.equal(resolveProjectLink('Projects/one/README.md', detail, { rooted: true, root: 'storage' }), null);
});

test('Storage and Mac projects keep their owning root rather than falling back to Box', () => {
  for (const root of ['storage', 'mac']) {
    const project = { root, path: 'Retained/one', facts: { Folder: '/mnt/retained/one' } };
    for (const path of ['README.md', 'Retained/one/README.md', '/mnt/retained/one/README.md']) {
      assert.deepEqual(resolveProjectLink(path, project), { root, path: 'Retained/one/README.md' });
    }
  }
});

test('escaping, ambiguous, malformed and encoded traversal paths are inert', () => {
  for (const path of [
    '../one/README.md', '../two/README.md', 'docs/../../two/README.md',
    'Projects/one/../two/README.md', '/srv/loom/box/Projects/one/../two/README.md',
    '/srv/loom/box/Projects/one-other/README.md', '/etc/passwd', '/Projects/one/README.md',
    '%2e%2e/README.md', 'docs/%2e%2e/%2e%2e/README.md', '%252e%252e/README.md',
    'docs%5c..%5csecret.md', 'docs%00/file.md', 'docs%0a/file.md', 'docs\\file.md', 'file.md\n',
    'file%ZZ.md', 'file.md?download=1', '//example.test/file.md', '%2f%2fexample.test/file.md',
    'file:///srv/loom/box/Projects/one/README.md', 'javascript:alert(1)', 'java%73cript:alert(1)',
    'data:text/html,unsafe', 'mailto:person@example.test', 'loom-file:secret', '~/loom-box/file.md', '#section', '', null,
  ]) assert.equal(resolveProjectLink(path, detail), null, String(path));
});

test('missing or invalid registered project scope never becomes a guessed local location', () => {
  for (const project of [undefined, {}, { root: 'box' }, { path: '/outside' }, { path: '../outside' }, { path: 'Projects/../one' }]) {
    assert.equal(resolveProjectLink('README.md', project), null);
  }
  assert.deepEqual(resolveProjectLink('README.md', { root: 'box', path: '' }), { root: 'box', path: 'README.md' });
  assert.equal(resolveProjectLink('/srv/loom/box/Projects/one/README.md', { ...detail, facts: {} }), null);
});

test('only explicit HTTP(S) external links are handed to the external owner', () => {
  const url = 'https://example.test/search?q=a%26b#part';
  assert.equal(projectExternalURL(url), url);
  assert.deepEqual(resolveProjectLink(url, {}), { external: url });
  assert.deepEqual(resolveProjectLink('http://example.test', detail), { external: 'http://example.test/' });
  for (const url of ['https:example.test', '//example.test', 'javascript:alert(1)', 'file:///etc/passwd', 'https://user:password@example.test', 'https://example.test\n/path']) {
    assert.equal(projectExternalURL(url), null);
    assert.equal(resolveProjectLink(url, detail), null);
  }
});

test('inline code links only path-shaped values, not commands or placeholders', () => {
  for (const path of ['README.md', '.gitignore', '.project/STATE.md', 'src/main.js', 'Makefile', 'docs/']) {
    assert.ok(resolveProjectLink(path, detail, { code: true }), path);
  }
  for (const path of ['git status', 'npm run build', 'cat docs/file.md', 'loom service restart', 'status', '$HOME/file.md', 'file.md;restart', 'docs/*.md', '$(cat secret)', '<project>/README.md']) {
    assert.equal(resolveProjectLink(path, detail, { code: true }), null, path);
  }
});

test('previews keep word or sentence boundaries, with no loss of the full source', () => {
  assert.equal(projectProsePreview('  First\n\nsecond.  '), 'First second.');
  const sentence = 'A complete useful first sentence for the reader.';
  assert.equal(projectProsePreview(sentence + ' More words. '.repeat(30), 55), sentence);
  assert.equal(projectProsePreview('one two three four five', 12), 'one two...');
  assert.equal(projectProsePreview('averylongfilename.md', 8), 'averylongfilename.md');
});
test('plain prose paths become bounded project navigation, not URL fragments or commands',()=>{
 const value='Use vault/START.md and docs/review.md#Plan. Not https://example.test/docs/foo.md or ../outside.md or v0.9.0.';
 assert.deepEqual(projectProsePaths(value,detail).map(p=>p.target),[file('vault/START.md'),file('docs/review.md',{heading:'Plan'})]);
});

function fakeNode(tag) {
  return { tagName: tag.toUpperCase(), children: [], attributes: {}, dataset: {}, classList: { add() {} },
    append(...children) { this.children.push(...children); },
    setAttribute(key, value) { this.attributes[key] = value; },
    querySelectorAll() { return []; },
    set innerHTML(value) { this.html = value;this.textContent = value.replace(/<[^>]*>/g, ''); },
    get innerHTML() { return this.html; },
  };
}
function dom(t) {
  const previous = globalThis.document;globalThis.document = { createElement: fakeNode };
  t.after(() => { if (previous === undefined) delete globalThis.document;else globalThis.document = previous; });
}
const click = (extra = {}) => ({ preventDefault() { this.prevented = true; }, stopPropagation() { this.stopped = true; }, ...extra });

test('Sections.button forwards the event, retains no-argument callers and reports errors', async t => {
  dom(t);const notices = [], owner = { icon: () => fakeNode('i'), notice: value => notices.push(value) };
  let received, oldCalls = 0;
  const button = Sections.prototype.button.call(owner, 'Open', 'file', event => { received = event; });
  const event = click({ metaKey: true });await button.onclick(event);assert.equal(received, event);
  await Sections.prototype.button.call(owner, 'Old', 'file', () => oldCalls++).onclick(event);assert.equal(oldCalls, 1);
  await Sections.prototype.button.call(owner, 'Failed', 'file', () => { throw Error('Unavailable'); }).onclick(event);
  assert.deepEqual(notices, ['Unavailable']);
});

test('resource alternate clicks forward original events without running right-click commands', async () => {
  const calls = [], button = { onclick: event => calls.push([event, navigationIntent(event)]) };
  alternateProjectNavigation(button);
  for (const options of [{ button: 1 }, { key: 'Enter', metaKey: true }, { key: 'Enter', ctrlKey: true, shiftKey: true }]) {
    const event = click(options);
    if (event.button === 1) await button.onauxclick(event);else await button.onkeydown(event);
    assert.equal(calls.at(-1)[0], event);
    assert.deepEqual(calls.at(-1)[1], { newTab: true, background: event.button === 1 || Boolean(event.shiftKey) });
  }
  button.onauxclick(click({ button: 2 }));button.onkeydown(click({ key: 'Enter' }));assert.equal(calls.length, 3);
});

test('project dispatch keeps navigation intent and destination headings with their owner', () => {
  const calls = [], owner = {
    openFile: (...args) => calls.push(['file', ...args]), openFolder: (...args) => calls.push(['folder', ...args]),
    api: { external: (...args) => calls.push(['external', ...args]) },
  };
  const intent = { newTab: true, background: true };
  Sections.prototype.openProjectLink.call(owner, file('report.pdf', { heading: 'page=4' }), intent);
  Sections.prototype.openProjectLink.call(owner, file('repos', { directory: true }), intent);
  Sections.prototype.openProjectLink.call(owner, { external: 'https://example.test/' }, intent);
  assert.deepEqual(calls, [
    ['file', 'Projects/one/report.pdf', 'box', { ...intent, heading: 'page=4' }],
    ['folder', 'box', 'Projects/one/repos', intent], ['external', 'https://example.test/'],
  ]);
});

test('prose always passes parsed source through the existing sanitizer before insertion', t => {
  dom(t);
  const priorMarked = globalThis.marked, priorPurify = globalThis.DOMPurify, calls = [];
  globalThis.marked = marked;
  globalThis.DOMPurify = { sanitize: (html, options) => { calls.push({ html, options });return '<p>Safe retained prose.</p>'; } };
  t.after(() => {
    if (priorMarked === undefined) delete globalThis.marked;else globalThis.marked = priorMarked;
    if (priorPurify === undefined) delete globalThis.DOMPurify;else globalThis.DOMPurify = priorPurify;
  });
  const source = '[Safe](README.md) <img src=x onerror="unsafe()"><script>unsafe()</script>';
  const prose = projectProse(source, detail);
  assert.equal(calls.length, 1);assert.match(calls[0].html, /<script>/);
  assert.equal(prose.children[0].innerHTML, '<p>Safe retained prose.</p>');
  assert.deepEqual(calls[0].options.ALLOWED_ATTR, ['href', 'title']);
  for (const forbidden of ['script', 'style', 'iframe', 'img', 'form', 'input', 'button']) assert.equal(calls[0].options.ALLOWED_TAGS.includes(forbidden), false);
  assert.equal(calls[0].options.ALLOW_DATA_ATTR, false);assert.equal(calls[0].options.ALLOW_ARIA_ATTR, false);
  const full = 'A complete retained project paragraph. '.repeat(400) + 'Final retained words.';
  globalThis.DOMPurify.sanitize = () => `<p>${full}</p>`;
  const long = projectProse(full, detail), disclosure = long.children[0];
  assert.equal(disclosure.tagName, 'DETAILS');
  const [summary, body] = disclosure.children, [preview, label] = summary.children;
  assert.ok(preview.textContent.length > 0);assert.ok(preview.textContent.length < full.length);
  assert.equal(body.textContent, full);assert.equal(label.textContent, 'More');
  disclosure.open = true;disclosure.ontoggle();assert.equal(label.textContent, 'Less');
  disclosure.open = false;disclosure.ontoggle();assert.equal(label.textContent, 'More');
});
