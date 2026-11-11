export const treeEntryKey = entry => JSON.stringify([entry.root, entry.path, entry.member?.id ?? null]);

export class TreeSelection {
  constructor() {
    this.visible = new Map();
    this.keys = new Set();
    this.anchor = null;
  }

  sync(entries, current) {
    const previous = this.visible;
    this.visible = new Map(entries.map(entry => [treeEntryKey(entry), entry]));
    this.keys = new Set([...this.keys].filter(key => this.visible.has(key)));
    const key = current ? treeEntryKey(current) : null;
    if (key !== this.currentKey || (!previous.has(key) && this.visible.has(key))) {
      this.currentKey = key;
      // A foreground open initiated here must not collapse modifier selection.
      if (key !== this.openingKey) {
        this.keys = new Set(this.visible.has(key) ? [key] : []);
        this.anchor = this.visible.has(key) ? key : null;
      }
      this.openingKey = null;
    }
    if (!this.visible.has(this.anchor)) this.anchor = null;
  }

  opening(entry) { this.openingKey = treeEntryKey(entry); }
  has(entry) { return this.keys.has(treeEntryKey(entry)); }

  select(entry, { shiftKey = false, metaKey = false, ctrlKey = false, preserve = false } = {}) {
    const key = treeEntryKey(entry);
    if (!this.visible.has(key) || preserve && this.keys.has(key)) return;
    const additive = metaKey || ctrlKey;
    if (shiftKey && this.anchor && this.visible.has(this.anchor)) {
      const visible = [...this.visible.keys()];
      const from = visible.indexOf(this.anchor), to = visible.indexOf(key);
      const range = visible.slice(Math.min(from, to), Math.max(from, to) + 1);
      this.keys = new Set(additive ? [...this.keys, ...range] : range);
    } else {
      if (additive) {
        if (this.keys.has(key)) this.keys.delete(key);
        else this.keys.add(key);
      } else this.keys = new Set([key]);
      this.anchor = key;
    }
  }

  entries() {
    return [...this.visible].filter(([key, entry]) => this.keys.has(key) && !entry.isRoot)
      .map(([, { root, path, directory, member }]) => ({ root, path, directory, ...(member ? { member } : {}) }));
  }
}
