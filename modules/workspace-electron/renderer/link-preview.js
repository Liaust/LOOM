let nextPreviewID = 0;
const bounded = (value, max) => typeof value === 'string' ? value.slice(0, max) : '';

/** Delegated previews for [data-note-link] elements (including editor links
 * carrying data-note-reference as well). target(anchor) returns an
 * owner navigation descriptor or null. preview(target,{signal,maxChars}) must
 * perform a bounded, non-adopting read; this module never resolves or opens it. */
export class LinkPreview {
  constructor(container, { preview, target, delay = 450, maxChars = 1200 } = {}) {
    this.container = container; this.preview = preview; this.target = target;
    this.delay = Number.isFinite(delay) ? Math.max(200, Math.min(1500, delay)) : 450;
    this.maxChars = Number.isFinite(maxChars) ? Math.max(1, Math.min(2000, Math.floor(maxChars))) : 1200;
    this.events = new AbortController();
    this.popup = container.ownerDocument.createElement('div');
    this.popup.className = 'note-link-preview'; this.popup.id = `note-link-preview-${++nextPreviewID}`;
    this.popup.setAttribute('role', 'tooltip'); this.popup.setAttribute('popover', 'manual');
    const options = { signal: this.events.signal };
    const anchor = value => { const found = value?.closest?.('[data-note-link]'); return found && container.contains(found) ? found : null; };
    container.addEventListener('pointerover', event => {
      const next = anchor(event.target); if (next === anchor(event.relatedTarget)) return;
      this.hovered = next; this.schedule(next || this.focused);
    }, options);
    container.addEventListener('pointerout', event => {
      const next = anchor(event.relatedTarget); if (next === anchor(event.target)) return;
      this.hovered = next;
      if (this.popup.contains(event.relatedTarget)) return;
      this.schedule(next || this.focused);
    }, options);
    container.addEventListener('focusin', event => { this.focused = anchor(event.target); this.schedule(this.focused); }, options);
    container.addEventListener('focusout', event => { this.focused = anchor(event.relatedTarget); this.schedule(this.focused || this.hovered); }, options);
    this.popup.addEventListener('pointerleave', () => { if (!this.focused) this.cancel(); }, options);
    container.addEventListener('pointerdown', () => this.cancel(), options);
    const document = container.ownerDocument, view = document.defaultView;
    const outside=event=>{if(!container.contains(event.target)&&!this.popup.contains(event.target)){this.hovered=this.focused=null;this.cancel();}};
    document.addEventListener('pointerdown',outside,{...options,capture:true});
    document.addEventListener('focusin',outside,options);
    document.addEventListener('keydown', event => {
      if (event.key === 'Escape' && this.popup.isConnected&&!document.querySelector('dialog[open]')) { event.preventDefault(); event.stopPropagation(); }
      if (event.key === 'Escape' || event.key === 'Enter' || event.key === ' ') this.cancel();
    }, { ...options, capture: true });
    document.addEventListener('scroll', () => this.cancel(), { ...options, capture: true });
    view.addEventListener('resize', () => this.cancel(), options);
    view.addEventListener('blur', () => this.cancel(), options);
  }

  schedule(anchor) {
    if (this.destroyed || anchor === this.anchor) return;
    this.cancel();
    if (!anchor || typeof this.preview !== 'function') return;
    const target = this.target?.(anchor); if (!target) return;
    this.anchor = anchor;
    const request = new AbortController(); this.request = request;
    this.timer = setTimeout(async () => {
      if (!this.current(anchor, request)) return this.cancel();
      try {
        const result = await this.preview(target, { signal: request.signal, maxChars: this.maxChars });
        if (!this.current(anchor, request)) return;
        if (result == null) return this.cancel();
        const data = typeof result === 'string' ? { text: result } : result;
        const doc = anchor.ownerDocument, title = doc.createElement('strong'), body = doc.createElement('p'), qualification = doc.createElement('small');
        title.textContent = bounded(data.title, 200) || bounded(anchor.textContent, 200);
        body.textContent = bounded(data.text, this.maxChars) || 'No text preview available.';
        qualification.textContent = bounded(data.qualification, 300);
        this.popup.replaceChildren(title, body, qualification);
        this.show(anchor);
      } catch {
        if (!this.current(anchor, request)) return;
        const body = anchor.ownerDocument.createElement('p'); body.textContent = 'Preview unavailable.';
        this.popup.replaceChildren(body); this.show(anchor);
      }
    }, this.delay);
  }

  current(anchor, request) {
    return !this.destroyed && this.anchor === anchor && this.request === request && !request.signal.aborted && anchor.isConnected && Boolean(anchor.getClientRects().length);
  }

  show(anchor) {
    (anchor.closest('dialog') || anchor.ownerDocument.body).append(this.popup);
    this.popup.showPopover?.(); this.position();
    const ids = new Set((anchor.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean));
    ids.add(this.popup.id); anchor.setAttribute('aria-describedby', [...ids].join(' '));
  }

  position() {
    const rect = this.anchor.getBoundingClientRect(), size = this.popup.getBoundingClientRect(), view = this.container.ownerDocument.defaultView;
    const left = Math.max(8, Math.min(rect.left, view.innerWidth - size.width - 8));
    const top = Math.max(8, Math.min(rect.bottom + 8 + size.height <= view.innerHeight - 8 ? rect.bottom + 8 : rect.top - size.height - 8, view.innerHeight - size.height - 8));
    this.popup.style.left = `${left}px`; this.popup.style.top = `${top}px`;
  }

  cancel() {
    clearTimeout(this.timer); this.request?.abort(); this.request = null;
    if (this.anchor) {
      const ids = (this.anchor.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== this.popup.id);
      if (ids.length) this.anchor.setAttribute('aria-describedby', ids.join(' ')); else this.anchor.removeAttribute('aria-describedby');
    }
    if (this.popup.matches(':popover-open')) this.popup.hidePopover();
    this.popup.remove(); this.anchor = null;
  }

  destroy() { this.cancel(); this.destroyed = true; this.events.abort(); this.hovered = this.focused = null; }
}
