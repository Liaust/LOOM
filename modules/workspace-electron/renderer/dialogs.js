import { selectControl } from './choice-controls.js';

function descriptionView(text) {
  const container = document.createElement('div'); container.className = 'form-description';
  const lines = text.split('\n');
  if (lines.filter(line => line.trim()).every(line => /^[^:\n]{1,35}: /.test(line))) {
    const facts = document.createElement('dl'); facts.className = 'inspector-facts';
    for (const line of lines.filter(line => line.trim())) {
      const at = line.indexOf(': '), term = document.createElement('dt'), value = document.createElement('dd');
      term.textContent = line.slice(0, at); value.textContent = line.slice(at + 2); facts.append(term, value);
    }
    container.append(facts);
  } else container.textContent = text;
  return container;
}

export function form(title, fields, { submit = 'Save', description = '',content:extraContent } = {}) {
  const dialog = document.querySelector('#action-dialog');
  if (dialog.open) return Promise.reject(new Error('Finish the current action first.'));
  const inspect = fields.length === 0 && submit === 'Close';
  const content = document.createElement('form'); content.method = 'dialog'; content.noValidate = true;
  const heading = document.createElement('h2'); heading.textContent = title; heading.id = 'action-heading'; content.append(heading); dialog.setAttribute('aria-labelledby', heading.id);
  const body = document.createElement('div'); body.className = 'form-body'; content.append(body);
  if (description) body.append(descriptionView(description));
  if(extraContent)body.append(extraContent);
  const inputs = [];
  const validate = entry => {
    const valid = entry.choice ? entry.control.validate() : entry.control.checkValidity();
    const state = entry.control.validity || {};
    const message = valid ? '' : state.valueMissing ? `${entry.label} is required.` : state.rangeUnderflow ? `Enter a value of at least ${entry.control.min}.` : state.rangeOverflow ? `Enter a value of at most ${entry.control.max}.` : state.badInput || state.stepMismatch ? 'Enter a valid number.' : entry.choice ? 'Choose an available option.' : `Enter a valid ${entry.label.toLowerCase()}.`;
    entry.error.textContent = message; entry.error.hidden = valid;
    if (valid) { entry.target.removeAttribute('aria-invalid'); entry.target.removeAttribute('aria-errormessage'); }
    else { entry.target.setAttribute('aria-invalid', 'true'); entry.target.setAttribute('aria-errormessage', entry.error.id); }
    return valid;
  };
  for (const field of fields) {
    const row = document.createElement('div'); row.className = 'form-field';
    const label = document.createElement('label'); label.textContent = field.label;
    const fieldError = document.createElement('p'); fieldError.className = 'field-error'; fieldError.id = `action-field-error-${inputs.length}`; fieldError.hidden = true; fieldError.setAttribute('role', 'alert');
    const register = (control, choice = false) => {
      const target = choice ? control.querySelector('button') : control;
      target.setAttribute('aria-describedby', fieldError.id);
      const entry = { control, target, choice, error: fieldError, label: field.label }; inputs.push(entry);
      const update = () => { if (!fieldError.hidden) validate(entry); };
      control.addEventListener('input', update); control.addEventListener('change', update);
      label.append(control); row.append(label, fieldError); body.append(row);
    };
    if (field.options) {
      register(selectControl({ ...field, items: field.options, required: field.required !== false }), true); continue;
    }
    const input = document.createElement(field.type === 'textarea' ? 'textarea' : 'input');
    input.name = field.name;
    if (field.type !== 'textarea') input.type = field.type || 'text';
    input.value = field.value ?? ''; input.required = field.required !== false; input.spellcheck = false;
    if(field.readOnly){input.readOnly=true;input.required=false;}
    if (field.type === 'checkbox') { input.value = 'on'; input.checked = field.value === true; input.required = false; label.classList.add('checkbox-field'); }
    if (field.placeholder) input.placeholder = field.placeholder;
    if (field.min !== undefined) input.min = field.min;
    register(input);
  }
  const error = document.createElement('p'); error.className = 'form-error'; error.setAttribute('role', 'status'); error.hidden = true;
  const actions = document.createElement('div'); actions.className = 'form-actions';
  if (!inspect) { const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = 'Cancel'; cancel.onclick = () => dialog.close(); actions.append(cancel); }
  const confirm = document.createElement('button'); confirm.type = 'submit'; confirm.className = inspect ? '' : 'primary'; confirm.textContent = submit;
  actions.append(confirm); content.append(error, actions); dialog.replaceChildren(content);
  const fingerprint = () => JSON.stringify([...new FormData(content)]), initial = fingerprint();
  const dismiss = () => { if (fingerprint() === initial) dialog.close(); else { error.textContent = 'Unsaved input retained. Continue editing, or choose Cancel to discard it.'; error.hidden = false; } };
  return new Promise(resolve => {
    let value = null;
    const events = new AbortController();
    content.onsubmit = event => { event.preventDefault(); const invalid = inputs.filter(entry => !validate(entry)); if (invalid.length) { invalid[0].control.focus(); return; } value = Object.fromEntries(new FormData(content)); dialog.close(); };
    dialog.addEventListener('cancel', event => { event.preventDefault(); dismiss(); }, { signal: events.signal });
    let outside = false;
    dialog.addEventListener('pointerdown', event => { outside = event.target === dialog; }, { signal: events.signal });
    dialog.addEventListener('click', event => { if (outside && event.target === dialog) dismiss(); outside = false; }, { signal: events.signal });
    dialog.addEventListener('close', () => { events.abort(); resolve(value); }, { once: true });
    dialog.showModal(); content.querySelector('input:not([type=hidden]),textarea,button')?.focus();
  });
}

export async function confirmAction(title, description, submit = 'Continue') {
  return Boolean(await form(title, [], { submit, description }));
}
