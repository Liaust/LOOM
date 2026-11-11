export function referenceParts(input) {
  if (typeof input !== 'string' || input.length > 8192 || /[\0\r\n\\]/.test(input)) throw Error('Invalid file reference.');
  let value = input.trim().replace(/^!?\[\[|\]\]$/g, '').split('|')[0];
  try { value = decodeURIComponent(value); } catch { throw Error('Invalid encoded file reference.'); }
  if (/^https?:\/\//i.test(value)) return { external: new URL(value).href };
  if (/^[a-z][\w+.-]*:/i.test(value) || /[\0\\]/.test(value)) throw Error('That link scheme is not allowed.');
  const at = value.indexOf('#');
  return { target: at < 0 ? value : value.slice(0, at), heading: at < 0 ? '' : value.slice(at + 1) };
}
