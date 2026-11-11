import { navigationIntent } from './navigation-intent.js';

const unsafe = value => /[\x00-\x1f\x7f\\]/.test(value);
const scheme = value => /^[a-z][\w+.-]*:/i.test(value);
const inside = (path, root) => !root || path === root || path.startsWith(root + '/');
const canonical = value => typeof value === 'string' && !unsafe(value) && !value.startsWith('/') &&
  (value === '' || value.split('/').every(part => part && part !== '.' && part !== '..'));

export function projectExternalURL(value) {
  if (typeof value !== 'string' || unsafe(value) || !/^https?:\/\//i.test(value.trim())) return null;
  try {
    const url = new URL(value.trim());
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : null;
  } catch { return null; }
}

// Prose is relative to the registered project, not a source document. Resource
// paths have already been mapped by the owner and must not be prefixed again.
export function resolveProjectLink(reference, detail, { rooted = false, root = detail?.root || 'box', code = false } = {}) {
  if (typeof reference !== 'string' || reference.length > 8192 || unsafe(reference)) return null;
  const value = reference.trim();
  if (!value) return null;
  const external = projectExternalURL(value);
  if (external) return { external };
  if (scheme(value) || value.startsWith('//') || value.includes('?')) return null;
  const hash = value.indexOf('#');
  let target, heading;
  try {
    target = decodeURIComponent(hash < 0 ? value : value.slice(0, hash));
    heading = hash < 0 ? '' : decodeURIComponent(value.slice(hash + 1));
  } catch { return null; }
  if (!target || unsafe(target) || unsafe(heading) || scheme(target) || target.startsWith('//') || target.startsWith('~') || /%[a-f0-9]{2}/i.test(target)) return null;
  if (code && (/[\s;&|<>`$(){}*?!="']/u.test(target) ||
    !target.includes('/') && !/\.[a-z0-9_-]+$/i.test(target) && !['README', 'LICENSE', 'Makefile', 'Dockerfile', 'justfile'].includes(target))) return null;
  const base = detail?.path, owningRoot = detail?.root || 'box';
  if (!canonical(base) || typeof owningRoot !== 'string' || !owningRoot || unsafe(owningRoot) || root !== owningRoot) return null;
  let relative = target;
  if (target.startsWith('/')) {
    const folder = detail?.facts?.Folder;
    if (typeof folder !== 'string' || unsafe(folder) || !folder.startsWith('/') || folder.startsWith('//')) return null;
    const absolute = folder.replace(/\/+$/, '');
    if (!inside(target, absolute)) return null;
    relative = target.slice(absolute.length).replace(/^\//, '');
  } else if (rooted || base && inside(target, base)) {
    if (!inside(target, base)) return null;
    relative = base ? target.slice(base.length).replace(/^\//, '') : target;
  }
  const parts = base ? base.split('/') : [], floor = parts.length;
  for (const part of relative.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (parts.length === floor) return null;parts.pop(); }
    else parts.push(part);
  }
  const path = parts.join('/');
  return { root: owningRoot, path, ...(heading ? { heading } : {}), ...(target.endsWith('/') || path === base ? { directory: true } : {}) };
}

export function alternateProjectNavigation(element) {
  element.onmousedown = event => { if (event.button === 1) event.preventDefault(); };
  element.onauxclick = event => { if (event.button === 1) return element.onclick(event); };
  element.onkeydown = event => {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();event.stopPropagation();return element.onclick(event);
    }
  };
  return element;
}

export function projectProsePreview(value, limit = 240) {
  const text = String(value ?? '').replace(/\s+/gu, ' ').trim();
  if (text.length <= limit) return text;
  const prefix = text.slice(0, limit + 1);
  const sentence = [...prefix.matchAll(/[.!?](?=\s)/g)].at(-1);
  if (sentence && sentence.index >= limit / 3) return text.slice(0, sentence.index + 1);
  const boundary = prefix.lastIndexOf(' ');
  // Keep an indivisible filename/token intact; the summary also has a CSS clamp.
  const end = boundary > 0 ? boundary : text.indexOf(' ') < 0 ? text.length : text.indexOf(' ');
  return text.slice(0, end) + (end < text.length ? '...' : '');
}

export function projectProsePaths(text,detail){
  const paths=[];for(const match of String(text).matchAll(/(?<![\p{L}\p{N}_/:.\-])(?:\.\/|[\p{L}\p{N}_-]+\/)+[\p{L}\p{N}_.-]+\.[a-zA-Z][a-zA-Z0-9_-]*(?:#[\p{L}\p{N}_=-]+)?/gu)){
    const target=resolveProjectLink(match[0],detail,{code:true});if(target&&!target.external)paths.push({from:match.index,to:match.index+match[0].length,text:match[0],target});
  }return paths;
}

export function projectProse(value, detail, { navigate, failed = () => {} } = {}) {
  const node = document.createElement('div');node.className = 'project-prose';
  const body = document.createElement('div');
  const source = String(value ?? '');
  body.innerHTML = DOMPurify.sanitize(marked.parse(source), {
    ALLOWED_TAGS: ['p', 'strong', 'em', 'code', 'pre', 'a', 'ul', 'ol', 'li', 'br', 'blockquote', 'h2', 'h3', 'h4'],
    ALLOWED_ATTR: ['href', 'title'], ALLOW_DATA_ATTR: false, ALLOW_ARIA_ATTR: false,
  });
  for(const titled of body.querySelectorAll('[title]')){titled.dataset.tooltip=titled.getAttribute('title');titled.removeAttribute('title');}
  const bind = (anchor, target) => {
    // No prose URL is left as a browser navigation into the privileged renderer.
    anchor.setAttribute('href', '#');anchor.classList.add('base-link');
    anchor.dataset.tooltip = target.external || `${target.root}/${target.path}`;
    anchor.onclick = event => {
      event.preventDefault();
      return Promise.resolve().then(() => navigate?.(target, navigationIntent(event))).catch(failed);
    };
    alternateProjectNavigation(anchor);
  };
  for (const anchor of body.querySelectorAll('a')) {
    const target = resolveProjectLink(anchor.getAttribute('href'), detail);
    if (target) bind(anchor, target);
    else anchor.replaceWith(...anchor.childNodes);
  }
  for (const code of body.querySelectorAll('code')) {
    if (code.closest('pre,a')) continue;
    const target = resolveProjectLink(code.textContent, detail, { code: true });
    if (!target) continue;
    const anchor = document.createElement('a');code.replaceWith(anchor);anchor.append(code);bind(anchor, target);
  }
  const linkPlain=parent=>{for(const child of [...parent.childNodes||[]]){
    if(child.nodeType===3){const paths=projectProsePaths(child.textContent,detail);if(!paths.length)continue;const parts=[];let start=0;for(const item of paths){parts.push(child.textContent.slice(start,item.from));const anchor=document.createElement('a');anchor.textContent=item.text;bind(anchor,item.target);parts.push(anchor);start=item.to;}parts.push(child.textContent.slice(start));child.replaceWith(...parts);}
    else if(child.nodeType===1&&!['A','CODE','PRE'].includes(child.tagName))linkPlain(child);
  }};linkPlain(body);
  if (source.length > 1000 || source.split('\n').length > 10) {
    const disclosure = document.createElement('details');disclosure.className = 'project-prose-disclosure';
    const summary = document.createElement('summary'), preview = document.createElement('span'), label = document.createElement('small');
    preview.className = 'project-prose-preview';preview.textContent = projectProsePreview(body.textContent);
    label.className = 'project-prose-toggle';label.textContent = 'More';summary.append(preview, label);
    disclosure.ontoggle = () => { label.textContent = disclosure.open ? 'Less' : 'More'; };
    disclosure.append(summary, body);node.append(disclosure);
  } else node.append(body);
  return node;
}
