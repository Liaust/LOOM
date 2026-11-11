let sequence = 0, active;

const element = (tag, cls, text) => { const n = document.createElement(tag); n.className = cls; if (text !== undefined) n.textContent = text; return n; };
const symbol = name => { const holder = element('span', 'choice-symbol'), icon = document.createElement('i'); icon.dataset.lucide = name; holder.append(icon); globalThis.lucide?.createIcons({ root: holder, attrs: { 'aria-hidden': 'true' } }); return holder; };

// A short-lived top-layer surface; it observes only while a picker is open.
function surface(anchor, { role, label, minWidth = 180, restore = true } = {}) {
  active?.close(false);
  const popup = element('div', 'choice-popup'); popup.id = `choice-${++sequence}`;
  popup.setAttribute('role', role); popup.setAttribute('aria-label', label);
  popup.setAttribute('popover', 'manual');
  (anchor.closest('dialog') || document.body).append(popup);
  const controller = new AbortController(), options = { signal: controller.signal };
  const position = () => {
    if (!anchor.isConnected) return close(false);
    const rect = anchor.getBoundingClientRect(), width = Math.min(Math.max(minWidth, rect.width), innerWidth - 16);
    popup.style.width = `${width}px`;
    const below = innerHeight - rect.bottom - 12, above = rect.top - 12;
    popup.style.maxHeight = `${Math.max(60, Math.min(300, Math.max(below, above)))}px`;
    const height = popup.offsetHeight;
    popup.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - width - 8))}px`;
    popup.style.top = `${Math.max(8, below >= height || below >= above ? rect.bottom + 4 : rect.top - height - 4)}px`;
  };
  const close = (focus = restore) => {
    if (!popup.isConnected) return;
    controller.abort(); popup.remove(); anchor.setAttribute('aria-expanded', 'false'); anchor.removeAttribute('aria-controls'); anchor.removeAttribute('aria-activedescendant');
    if (active?.popup === popup) active = null;
    if (focus && anchor.isConnected) anchor.focus({ preventScroll: true });
  };
  popup.showPopover();
  anchor.setAttribute('aria-expanded', 'true'); anchor.setAttribute('aria-controls', popup.id);
  document.addEventListener('pointerdown', event => { if (!popup.contains(event.target) && !anchor.contains(event.target)) close(false); }, { ...options, capture: true });
  document.addEventListener('focusin', event => { if (!popup.contains(event.target) && !anchor.contains(event.target)) close(false); }, options);
  window.addEventListener('resize', position, options);
  document.addEventListener('scroll', position, { ...options, capture: true });
  window.addEventListener('blur', () => close(false), options);
  anchor.closest('dialog')?.addEventListener('close', () => close(false), options);
  active = { popup, close }; position();
  return { popup, close, position };
}

export function choiceMenu(anchor, items, onChoose, { label = 'Choose an option', icon } = {}) {
  if (active?.popup && anchor.getAttribute('aria-controls') === active.popup.id) { active.close(); return; }
  const { popup, close, position } = surface(anchor, { role: 'menu', label });
  anchor.setAttribute('aria-haspopup', 'menu');
  for (const item of items) {
    const button = element('button', 'choice-item'); button.type = 'button'; button.disabled = Boolean(item.disabled);
    button.setAttribute('role', 'menuitemradio'); button.setAttribute('aria-checked', String(Boolean(item.selected)));
    if (icon && item.icon) button.append(icon(item.icon));
    const check = element('span', 'choice-check'); if (item.selected) check.append(symbol('check'));
    button.append(element('span', '', item.label), check);
    button.onclick = () => { close(); onChoose(item.value); }; popup.append(button);
  }
  popup.addEventListener('keydown', event => {
    const buttons = [...popup.querySelectorAll('button:not(:disabled)')], index = buttons.indexOf(document.activeElement);
    const next = event.key === 'ArrowDown' ? buttons[(index + 1) % buttons.length] : event.key === 'ArrowUp' ? buttons[(index - 1 + buttons.length) % buttons.length] : event.key === 'Home' ? buttons[0] : event.key === 'End' ? buttons.at(-1) : null;
    if (next) { event.preventDefault(); event.stopPropagation(); next.focus(); }
    if (event.key === 'Escape' || event.key === 'Tab') { if (event.key === 'Escape') event.preventDefault(); event.stopPropagation(); close(); }
  });
  position(); (popup.querySelector('[aria-checked=true]:not(:disabled)') || popup.querySelector('button:not(:disabled)'))?.focus();
}

export function selectControl({ name, label, items, value, required = true }) {
  const holder = element('div', 'choice-select'), input = element('input', ''); input.type = 'hidden'; input.name = name;
  const choices = items.map(item => typeof item === 'object' ? { ...item, value: String(item.value ?? item.label) } : { value: String(item), label: String(item) });
  input.value = String(value ?? choices[0]?.value ?? '');
  const button = element('button', 'choice-select-trigger'); button.type = 'button'; button.setAttribute('aria-label', label); button.setAttribute('aria-haspopup', 'menu'); button.setAttribute('aria-expanded', 'false');
  const render = () => { button.replaceChildren(element('span', '', choices.find(item => item.value === input.value)?.label || 'Choose…'), symbol('chevron-down')); };
  const choose = next => { input.value = next; render(); input.dispatchEvent(new Event('change', { bubbles: true })); };
  button.onclick = () => choiceMenu(button, choices.map(item => ({ ...item, selected: item.value === input.value })), choose, { label });
  button.onkeydown = event => { if (['ArrowDown', 'ArrowUp'].includes(event.key)) { event.preventDefault(); button.click(); } };
  holder.validate = () => !required || choices.some(item => item.value === input.value && !item.disabled);
  holder.focus = () => button.focus(); holder.append(input, button); render(); return holder;
}

export function attachSuggestions(input, { items, load, onChoose }) {
  input.setAttribute('role', 'combobox'); input.setAttribute('aria-autocomplete', 'list'); input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-haspopup', 'listbox'); input.autocomplete = 'off';
  let current, choices = [], index = -1, timer, generation = 0;
  const dismiss = () => { generation++; clearTimeout(timer); current?.close(false); current = null; };
  const render = candidates => {
    if (!input.isConnected || document.activeElement !== input) return;
    choices = [...new Set(candidates)].filter(name => name.toLowerCase().includes(input.value.toLowerCase())).slice(0, 30);
    if (!choices.length) { current?.close(false); current = null; return; }
    if (!current?.popup.isConnected) current = surface(input, { role: 'listbox', label: `${input.getAttribute('aria-label')} suggestions`, minWidth: 240, restore: false });
    index = Math.min(index, choices.length - 1);
    current.popup.replaceChildren(...choices.map((name, i) => {
      const row = element('div', 'choice-item', name); row.id = `${current.popup.id}-${i}`; row.setAttribute('role', 'option'); row.setAttribute('aria-selected', String(i === index));
      row.onpointerdown = event => event.preventDefault();
      row.onclick = () => { if(current?.popup.isConnected)choose(i); }; return row;
    }));
    const selected = current.popup.querySelector('[aria-selected=true]');
    if (selected) { input.setAttribute('aria-activedescendant', selected.id); selected.scrollIntoView({ block: 'nearest' }); } else input.removeAttribute('aria-activedescendant');
    current.position();
  };
  const choose = i => { const value = choices[i]; if (value === undefined) return; input.value = value; dismiss(); onChoose?.(value); };
  const update = () => {
    const request = ++generation; clearTimeout(timer); index = -1; render(items());
    if (load) timer = setTimeout(async () => { try { const extra = await load(input.value); if (request === generation) render([...items(), ...extra]); } catch { /* Local suggestions remain available offline. */ } }, 150);
  };
  input.addEventListener('focus', update); input.addEventListener('input', update);
  input.addEventListener('blur', () => { dismiss(); });
  input.addEventListener('keydown', event => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); event.stopPropagation();
      if (!current?.popup.isConnected) render(items());
      index = choices.length ? (index + (event.key === 'ArrowDown' ? 1 : -1) + choices.length) % choices.length : -1;
      render(choices);
    } else if (event.key === 'Enter' && current?.popup.isConnected && index >= 0) { event.preventDefault(); event.stopImmediatePropagation(); choose(index); }
    else if (event.key === 'Escape' && current?.popup.isConnected) { event.preventDefault(); event.stopImmediatePropagation(); dismiss(); }
  });
}
