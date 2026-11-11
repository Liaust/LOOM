export function tooltipPosition(anchor, size, viewport) {
  const gap = 8, left = Math.max(gap, Math.min(anchor.left + anchor.width / 2 - size.width / 2, viewport.width - size.width - gap));
  const below = anchor.bottom + gap;
  return { left, top: Math.max(gap, below + size.height <= viewport.height - gap ? below : anchor.top - size.height - gap) };
}

export function installTooltips() {
  const tooltip = document.createElement('div');
  tooltip.id = 'workspace-tooltip'; tooltip.className = 'app-tooltip';
  tooltip.setAttribute('role', 'tooltip'); tooltip.setAttribute('popover', 'manual');
  const events = new AbortController(), options = { signal: events.signal };
  let anchor, hovered, focused, showTimer, hideTimer;
  const target = node => node?.closest?.('[data-tooltip]');
  const hide = () => {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    if (anchor) {
      const ids = (anchor.getAttribute('aria-describedby') || '').split(/\s+/).filter(id => id && id !== tooltip.id);
      if (ids.length) anchor.setAttribute('aria-describedby', ids.join(' ')); else anchor.removeAttribute('aria-describedby');
    }
    if (tooltip.matches(':popover-open')) tooltip.hidePopover();
    tooltip.remove(); anchor = null;
  };
  const show = element => {
    if (!element?.isConnected || !element.getClientRects().length || !element.dataset.tooltip?.trim() || element.getAttribute('aria-expanded') === 'true') return hide();
    hide(); anchor = element; tooltip.textContent = element.dataset.tooltip;
    (element.closest('dialog') || document.body).append(tooltip); tooltip.showPopover();
    const position = tooltipPosition(element.getBoundingClientRect(), tooltip.getBoundingClientRect(), { width: innerWidth, height: innerHeight });
    tooltip.style.left = `${position.left}px`; tooltip.style.top = `${position.top}px`;
    const ids = new Set((element.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean)); ids.add(tooltip.id);
    element.setAttribute('aria-describedby', [...ids].join(' '));
  };
  const schedule = element => {
    clearTimeout(hideTimer); clearTimeout(showTimer);
    if (!element) return hide();
    if (anchor === element) return;
    hide(); showTimer = setTimeout(() => { if (hovered === element || focused === element) show(element); }, 450);
  };
  const leave = () => { clearTimeout(showTimer); hideTimer = setTimeout(() => { if (!tooltip.matches(':hover') && !hovered && !focused) hide(); }, 120); };
  document.addEventListener('pointerover', event => {
    if (tooltip.contains(event.target)) return clearTimeout(hideTimer);
    const next = target(event.target); if (next === hovered) return;
    hovered = next; if (next) schedule(next); else if (!focused) leave();
  }, options);
  document.addEventListener('pointerout', event => {
    if (target(event.target) === target(event.relatedTarget) && !tooltip.contains(event.target)) return;
    if (tooltip.contains(event.relatedTarget)) return clearTimeout(hideTimer);
    hovered = target(event.relatedTarget);
    if (hovered) schedule(hovered); else if (!focused) leave();
  }, options);
  document.addEventListener('focusin', event => { focused = event.target.matches?.(':focus-visible') ? target(event.target) : null; if (focused) schedule(focused); }, options);
  document.addEventListener('focusout', () => { focused = null; if (!hovered) leave(); }, options);
  document.addEventListener('pointerdown', hide, { ...options, capture: true });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && anchor) { event.preventDefault(); event.stopPropagation(); }
    hide();
  }, { ...options, capture: true });
  document.addEventListener('scroll', hide, { ...options, capture: true });
  document.addEventListener('close', hide, { ...options, capture: true });
  window.addEventListener('resize', hide, options); window.addEventListener('blur', hide, options);
  const destroy = () => { hide(); events.abort(); };
  window.addEventListener('pagehide', destroy, { ...options, once: true });
  return { hide, destroy };
}
