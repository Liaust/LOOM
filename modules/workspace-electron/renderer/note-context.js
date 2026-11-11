import { noteOutline } from './note-outline.js';
import { LinkPreview } from './link-preview.js';
import { navigationIntent } from './navigation-intent.js';
import { referenceParts } from '../src/reference-parts.mjs';

let nextContextID = 0;
const tabs = [['details', 'Details', 'info'], ['outline', 'Outline', 'list-tree'], ['backlinks', 'Backlinks', 'corner-up-left'], ['outgoing', 'Outgoing', 'link']];

/** Owner-only boundary:
 * document: {root,path,text,graphError?}; graph: NoteGraph.links() result.
 * navigate(target,{newTab,background}) gets {root,path} or {root,from,reference}.
 * heading({text,level,line,from,to}) gets exact source offsets (line is 1-based).
 * preview(target,{signal,maxChars}) returns string | {text,title?,qualification?}
 *   | null, using a bounded, non-adopting read supplied by the owner.
 * details(document) may return an owner-rendered DOM node; detailsNode overrides
 *   it. No HTML strings are accepted. A persistent instance retains selectedTab;
 *   callers can select(tab) when restoring their own session preference.
 */
export class NoteContext {
  constructor(container, { icon, icons, navigate, heading, preview, details } = {}) {
    this.container = container; Object.assign(this, { icon, icons, navigate, heading, details });
    this.selectedTab = 'details'; this.targets = new WeakMap(); this.buttons = new Map(); this.panels = new Map();
    this.events = new AbortController();
    const id = `note-context-${++nextContextID}`;
    this.root = this.node('section', 'note-context'); this.root.setAttribute('aria-label', 'Note context');
    const bar = this.node('div', `note-context-tabs${icon ? ' note-context-tabs-icons' : ''}`); bar.setAttribute('role', 'tablist'); bar.setAttribute('aria-label', 'Note context');
    for (const [key, label, symbol] of tabs) {
      const button = this.node('button', 'note-context-tab'); button.type = 'button'; button.id = `${id}-${key}-tab`;
      button.setAttribute('role', 'tab'); button.setAttribute('aria-label', label); button.setAttribute('aria-controls', `${id}-${key}`); button.dataset.tooltip = label;
      if (icon) button.append(icon(symbol)); button.append(this.node('span', 'note-context-tab-label', label));
      button.addEventListener('click', () => this.select(key), { signal: this.events.signal });
      button.addEventListener('keydown', event => {
        const index = tabs.findIndex(tab => tab[0] === key);
        const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1;
        if (next < 0) return;
        event.preventDefault(); this.select(tabs[next][0]); this.buttons.get(tabs[next][0]).focus();
      }, { signal: this.events.signal });
      const panel = this.node('div', 'note-context-panel'); panel.id = `${id}-${key}`; panel.tabIndex = 0;
      panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', button.id);
      this.buttons.set(key, button); this.panels.set(key, panel); bar.append(button);
    }
    this.status = this.node('p', 'note-context-status'); this.status.setAttribute('role', 'status'); this.status.hidden = true;
    this.root.append(bar, ...this.panels.values(), this.status); container.append(this.root);
    this.previews = new LinkPreview(this.root, { preview, target: anchor => this.targets.get(anchor) });
    this.select('details'); icons?.(this.root);
    this.update({ document: null });
  }

  node(tag, className, text) {
    const node = this.container.ownerDocument.createElement(tag); if (className) node.className = className;
    if (text !== undefined) node.textContent = text; return node;
  }

  select(key) {
    if (this.destroyed || !this.buttons.has(key)) return;
    this.previews?.cancel(); this.selectedTab = key;
    for (const [name, button] of this.buttons) {
      button.setAttribute('aria-selected', String(name === key)); button.tabIndex = name === key ? 0 : -1;
      this.panels.get(name).hidden = name !== key;
    }
  }

  update({ document: value = null, graph = value?.graph ?? null, detailsNode } = {}) {
    if (this.destroyed) return;
    this.previews.cancel(); this.document = value; this.graph = graph;
    this.status.hidden = true;
    const details = this.panels.get('details');
    const content = detailsNode ?? (value && this.details?.(value));
    if (content?.nodeType) {
      if (details.firstChild !== content || details.childNodes.length !== 1) details.replaceChildren(content);
    } else if (!value) this.empty(details, 'No note selected.');
    else {
      const name = this.node('h3', 'note-context-name', value.entry?.name || value.path?.split('/').pop() || 'Note');
      details.replaceChildren(name, this.node('p', 'note-context-path', [value.root, value.path].filter(Boolean).join('/')));
    }
    const outline = this.panels.get('outline');
    if (value && typeof value.text === 'string' && /\.(?:md|markdown|mdown|mkdn?)$/i.test(value.path || '')) {
      if (this.outlineText !== value.text) { this.outlineText = value.text; this.outlineItems = noteOutline(value.text); }
      const list = this.node('ol', 'note-context-outline');
      for (const item of this.outlineItems) {
        const row = this.node('li'), button = this.node('button', 'note-context-heading', item.text || '(Untitled heading)');
        button.type = 'button'; button.dataset.level = String(item.level); button.dataset.line = String(item.line);
        button.disabled = typeof this.heading !== 'function'; button.onclick = () => this.run(this.heading, item); row.append(button); list.append(row);
      }
      if (list.childNodes.length) outline.replaceChildren(list); else this.empty(outline, 'No headings in this note.');
    } else this.empty(outline, value ? 'No Markdown outline.' : 'No note selected.');
    this.renderLinks('backlinks', value, graph); this.renderLinks('outgoing', value, graph);
    this.icons?.(this.root);
  }

  empty(panel, message) { panel.replaceChildren(this.node('p', 'note-context-empty', message)); }

  run(callback, ...args) {
    if (this.destroyed || typeof callback !== 'function') return;
    const current = this.document;
    const failed = () => {
      if (!this.destroyed && this.document === current) { this.status.textContent = 'Navigation unavailable.'; this.status.hidden = false; }
    };
    try { Promise.resolve(callback(...args)).catch(failed); } catch { failed(); }
  }

  renderLinks(kind, value, graph) {
    const panel = this.panels.get(kind);
    if (!value) return this.empty(panel, 'No note selected.');
    if (!graph) return this.empty(panel, value.graphError ? 'Link metadata unavailable.' : 'Link metadata not loaded.');
    const qualification = [graph.origin, graph.qualification || graph.coverage || 'Known links only; coverage not supplied.', graph.truncated ? 'Bounded result; more links may exist.' : '', ...(graph.warnings || []), graph.error ? 'Latest graph refresh unavailable.' : ''].filter(Boolean).join(' · ');
    const list = this.node('ul', 'note-context-links');
    const items = kind === 'backlinks' ? graph.backlinks : graph.forward;
    for (const item of items || []) {
      const reference = typeof item === 'string' ? item : item.target;
      if (kind === 'backlinks' ? !item.path : typeof reference !== 'string') continue;
      const target = kind === 'backlinks' ? { root: item.root || value.root, path: item.path } : { root: value.root, from: value.path, reference };
      const label = kind === 'backlinks' ? item.name || item.path.split('/').pop() : reference;
      const row = this.node('li'), button = this.node('button', 'note-context-link'); button.type = 'button';
      if (this.icon) button.append(this.icon(kind === 'backlinks' ? 'corner-up-left' : 'link'));
      const text = this.node('span', 'note-context-link-text'); text.append(this.node('span', 'note-context-link-name', label));
      if (kind === 'backlinks') text.append(this.node('small', '', `${target.root}/${target.path}${item.kind === 'local_pending' ? ' · Local pending' : ''}`));
      button.append(text); button.disabled = typeof this.navigate !== 'function';
      // Only valid internal references are previewable. Navigation still belongs
      // to the owner, including external URLs and ambiguous references.
      let previewable = kind === 'backlinks';
      if (!previewable) try { previewable = !referenceParts(reference).external; } catch { /* Owner reports invalid link navigation. */ }
      if (previewable && !button.disabled) { button.dataset.noteLink = ''; this.targets.set(button, target); }
      button.onclick = event => { event.preventDefault(); this.run(this.navigate, target, navigationIntent(event)); };
      button.onauxclick = event => { if (event.button === 1) { event.preventDefault(); this.run(this.navigate, target, navigationIntent(event)); } };
      button.onkeydown = event => {
        if (event.key !== 'Enter' || !(event.metaKey || event.ctrlKey)) return;
        event.preventDefault(); event.stopPropagation(); this.run(this.navigate, target, navigationIntent(event));
      };
      row.append(button); list.append(row);
    }
    panel.replaceChildren(this.node('p', 'note-context-qualification', qualification));
    if (list.childNodes.length) panel.append(list);
    else panel.append(this.node('p', 'note-context-empty', kind === 'backlinks' ? 'No known backlinks.' : 'No known outgoing links.'));
  }

  destroy() {
    if (this.destroyed) return;
    this.destroyed = true; this.previews.destroy(); this.events.abort(); this.root.remove();
    this.targets = new WeakMap(); this.document = this.graph = this.outlineItems = this.outlineText = null;
  }
}
