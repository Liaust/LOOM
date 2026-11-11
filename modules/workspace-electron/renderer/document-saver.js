// Coalesce typing, but never let an older completed save clear a newer draft.
export class DocumentSaver {
  constructor({ persist, write, saved = () => {}, failed = () => {}, delay = 750 }) {
    Object.assign(this, { persist, write, saved, failed, delay }); this.timers = new Map(); this.running = new Map();
  }
  schedule(document) {
    clearTimeout(this.timers.get(document));
    this.timers.set(document, setTimeout(() => { this.timers.delete(document); void this.save(document); }, this.delay));
  }
  async save(document) {
    clearTimeout(this.timers.get(document)); this.timers.delete(document);
    const previous = this.running.get(document);
    if (previous) { const ok = await previous; return ok && document.dirty ? this.save(document) : ok; }
    if (!document.dirty || !document.editable) return true;
    const text = document.text;
    const task = (async () => {
      try {
        await this.persist(document);
        const result = await this.write(document, text, document.base);
        Object.assign(document, { base: result.base, entry: result.entry, pending: result.pending, held: result.held, dirty: document.text !== text });
        if (document.dirty) await this.persist(document);
        this.saved(document, result); return true;
      } catch (error) { document.dirty = true; this.failed(document, error); return false; }
    })().finally(() => this.running.delete(document));
    this.running.set(document, task);
    return task;
  }
  async flush(documents) { return Promise.all(documents.map(document => this.save(document))); }
}
