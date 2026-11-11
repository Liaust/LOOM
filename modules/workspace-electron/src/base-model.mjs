import { parseDocument } from 'yaml';
import jsep from 'jsep';
import path from 'node:path';

const denied = new Set(['__proto__', 'prototype', 'constructor']);
const safeProperty = (object, name) => {
  if (denied.has(String(name))) throw Error('Unsafe property in Base expression.');
  return object && Object.hasOwn(object, name) ? object[name] : null;
};
export function yamlData(text) {
  if (typeof text !== 'string' || Buffer.byteLength(text) > 512 * 1024) throw Error('YAML view exceeds its 512 KiB limit.');
  const document = parseDocument(text, { uniqueKeys: true });
  if (document.errors.length) throw Error(document.errors[0].message);
  return document.toJS({ maxAliasCount: 32 });
}
export function noteProperties(record) {
  if(record.values){const ext=path.posix.extname(record.path).slice(1);return{root:record.root,path:record.path,note:record.values,file:{path:record.path,folder:path.posix.dirname(record.path),name:path.posix.basename(record.path,'.'+ext),ext,tags:record.tags||[],links:(record.forward||[]).map(r=>r.target),size:record.size??null,mtime:record.modified_at||null}};}
  const text = record.text || '';
  const match = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(text.slice(0, 65536));
  let note = {};
  if (match) {
    const parsed = yamlData(match[1]);
    if (parsed && !Array.isArray(parsed) && typeof parsed === 'object') note = parsed;
  }
  const tags = [...new Set([...(Array.isArray(note.tags) ? note.tags : typeof note.tags === 'string' ? note.tags.split(/[\s,]+/) : []),
    ...[...text.matchAll(/(?:^|\s)#([\p{L}\p{N}_/-]+)/gu)].map(m => m[1])].filter(t => typeof t === 'string').map(t => t.replace(/^#/, '')))];
  const links = [...text.matchAll(/\[\[([^\]\n|#]+)(?:[^\]\n]*)\]\]/g)].map(m => m[1]);
  const ext = path.posix.extname(record.path).slice(1);
  return { root: record.root, path: record.path, note, file: { path: record.path, folder: path.posix.dirname(record.path),
    name: path.posix.basename(record.path, '.' + ext), ext, tags, links, size: Buffer.byteLength(text),
    mtime: record.source?.file?.updated_at || record.source?.modified_at || null } };
}

// Parse a small expression language; never evaluate source as JavaScript or
// invoke methods read from a note, object prototype, URL or filesystem.
export function compileExpression(source) {
  if (typeof source !== 'string' || source.length > 4096) throw Error('Invalid Base expression.');
  const ast = jsep(source);
  return (row, context = {}, formula = () => null) => {
    let remaining = 1024;
    const visit = (node, depth = 0) => {
      if (--remaining < 0 || depth > 32) throw Error('Base expression is too complex.');
      const go = n => visit(n, depth + 1);
      switch (node.type) {
        case 'Literal': return node.value;
        case 'Identifier':
          if (node.name === 'file') return row.file;
          if (node.name === 'note') return row.note;
          if (node.name === 'this') return context;
          if (node.name === 'null') return null;
          return safeProperty(row.note, node.name);
        case 'ThisExpression': return context;
        case 'ArrayExpression': return node.elements.map(go);
        case 'MemberExpression': {
          const property = node.computed ? go(node.property) : node.property.name;
          if (node.object.type === 'Identifier' && node.object.name === 'formula') return formula(property);
          return safeProperty(go(node.object), property);
        }
        case 'UnaryExpression': {
          const value = go(node.argument);
          if (node.operator === '!') return !value;
          if (node.operator === '-') return -Number(value);
          if (node.operator === '+') return Number(value);
          break;
        }
        case 'BinaryExpression': {
          const a = go(node.left);
          if (node.operator === '&&') return Boolean(a) && Boolean(go(node.right));
          if (node.operator === '||') return Boolean(a) || Boolean(go(node.right));
          const b = go(node.right);
          switch (node.operator) {
            case '==': case '===': return JSON.stringify(a) === JSON.stringify(b);
            case '!=': case '!==': return JSON.stringify(a) !== JSON.stringify(b);
            case '>': return a > b; case '<': return a < b; case '>=': return a >= b; case '<=': return a <= b;
            case '+': return a + b; case '-': return a - b; case '*': return a * b; case '/': return a / b; case '%': return a % b;
          }
          break;
        }
        case 'ConditionalExpression': return go(node.test) ? go(node.consequent) : go(node.alternate);
        case 'CallExpression': {
          const args = node.arguments.map(go), callee = node.callee;
          if (callee.type === 'Identifier') {
            switch (callee.name) {
              case 'if': return args[0] ? args[1] : args[2] ?? null;
              case 'list': return Array.isArray(args[0]) ? args[0] : args[0] == null ? [] : [args[0]];
              case 'date': return Date.parse(args[0]);
              case 'now': return Date.now();
              case 'today': return new Date().setHours(0, 0, 0, 0);
              case 'min': return Math.min(...args); case 'max': return Math.max(...args);
            }
          } else if (callee.type === 'MemberExpression') {
            const value = go(callee.object), method = callee.computed ? go(callee.property) : callee.property.name;
            if (denied.has(method)) throw Error('Unsafe Base method.');
            if (value === row.file) {
              if (method === 'inFolder') return value.folder === args[0] || value.folder.startsWith(String(args[0]).replace(/\/$/, '') + '/');
              if (method === 'hasTag') return value.tags.some(t => args.some(a => t === a || t.startsWith(String(a) + '/')));
              if (method === 'hasLink') return value.links.includes(args[0]?.path || args[0]);
            }
            if (method === 'contains' && (typeof value === 'string' || Array.isArray(value))) return value.includes(args[0]);
            if (method === 'containsAny' && Array.isArray(value)) return args.some(a => value.includes(a));
            if (method === 'containsAll' && Array.isArray(value)) return args.every(a => value.includes(a));
            if (method === 'isEmpty') return value == null || value === '' || Array.isArray(value) && value.length === 0;
            if (typeof value === 'string') {
              if (method === 'startsWith') return value.startsWith(args[0]);
              if (method === 'endsWith') return value.endsWith(args[0]);
              if (method === 'lower') return value.toLowerCase();
              if (method === 'upper') return value.toUpperCase();
            }
            if (typeof value === 'number' && method === 'toFixed' && Number.isInteger(args[0]) && args[0] >= 0 && args[0] <= 20) return value.toFixed(args[0]);
          }
          throw Error('This Base uses an unsupported function; its source is preserved.');
        }
      }
      throw Error(`Unsupported Base expression: ${node.type}${node.operator ? ' ' + node.operator : ''}.`);
    };
    return visit(ast);
  };
}

function filterFunction(filter, depth = 0) {
  if (depth > 20) throw Error('Base filter is too deeply nested.');
  if (filter == null) return () => true;
  if (typeof filter === 'string') return compileExpression(filter);
  if (typeof filter !== 'object' || Array.isArray(filter) || Object.keys(filter).length !== 1) throw Error('Invalid Base filter.');
  const [operator] = Object.keys(filter), values = filter[operator];
  if (!['and', 'or', 'not'].includes(operator) || !Array.isArray(values)) throw Error('Unsupported Base filter.');
  if (values.length > 100) throw Error('Base filter has too many terms.');
  const terms = values.map(value => filterFunction(value, depth + 1));
  return (row, context, formula) => operator === 'or' ? terms.some(fn => fn(row, context, formula)) : operator === 'not'
    ? !terms.some(fn => fn(row, context, formula)) : terms.every(fn => fn(row, context, formula));
}

// Narrow reads only for a folder condition every matching row must satisfy.
// OR, negation and dynamic arguments are deliberately not inferred.
export function baseCatalogScope(text,viewIndex=0){
  const base=yamlData(text),folders=[];
  const expression=node=>{
    if(node?.type==='BinaryExpression'&&node.operator==='&&')return expression(node.left)||expression(node.right);
    if(node?.type!=='CallExpression'||node.arguments.length!==1)return '';
    const c=node.callee,p=node.arguments[0];
    if(c.type!=='MemberExpression'||c.computed||c.object.type!=='Identifier'||c.object.name!=='file'||c.property.name!=='inFolder'||p.type!=='Literal'||typeof p.value!=='string')return '';
    const folder=p.value.replace(/\/$/,'');return folder&&!folder.startsWith('/')&&!folder.includes('\\')&&!folder.includes('\0')&&folder.split('/').every(p=>p&&p!=='.'&&p!=='..')?folder:'';
  };
  const inspect=(filter,depth=0)=>{if(depth>20)return;if(typeof filter==='string'){const folder=expression(jsep(filter));if(folder)folders.push(folder);}else if(filter&&typeof filter==='object'&&!Array.isArray(filter)&&Object.keys(filter).length===1&&Array.isArray(filter.and))for(const child of filter.and)inspect(child,depth+1);};
  inspect(base?.filters);inspect(base?.views?.[viewIndex]?.filters);return folders.sort((a,b)=>b.length-a.length)[0]||'';
}

export function queryBase(text, records, { viewIndex = 0, offset = 0, limit = 100, basePath = '', query='',qualification,sort:overrideSort } = {}) {
  const base = yamlData(text);
  if (!base || typeof base !== 'object' || !Array.isArray(base.views) || base.views.length > 50) throw Error('This Base needs a bounded views list.');
  const view = base.views[viewIndex];
  if (!view || !['table', 'cards'].includes(view.type)) throw Error('This Base view type is not supported yet. Its YAML remains editable.');
  if (view.summaries || base.summaries) throw Error('Summary expressions are not supported yet. The source is preserved.');
  const groupProperty=typeof view.groupBy==='string'?view.groupBy:view.groupBy?.property;
  const expressions = new Map(Object.entries(base.formulas || {}).map(([name, source]) => [name, compileExpression(source)]));
  const global = filterFunction(base.filters), selected = filterFunction(view.filters);
  const context = { file: { path: basePath, folder: path.posix.dirname(basePath) } };
  const columns = Array.isArray(view.order) && view.order.length ? view.order.slice(0, 20) : ['file.name', 'file.folder'];
  if (columns.some(c => typeof c !== 'string')) throw Error('Invalid Base columns.');
  const sorting = overrideSort || view.sort || [];
  if (!Array.isArray(sorting) || sorting.length > 10 || sorting.some(s => typeof s?.property !== 'string')) throw Error('Invalid Base sort.');
  const requested = [...new Set([...columns, ...sorting.map(s => s.property),...groupProperty?[groupProperty]:[]])];
  const rows = [];
  let invalid = 0;
  for (const record of records) {
    if (record.deleted || record.directory) continue;
    let row;
    try { row = noteProperties(record); } catch { invalid++; continue; }
    const resolving = new Set();
    const formula = name => {
      if (!expressions.has(name) || resolving.has(name)) throw Error('Unknown or circular Base formula.');
      resolving.add(name);
      try { return expressions.get(name)(row, context, formula); } finally { resolving.delete(name); }
    };
    if (!global(row, context, formula) || !selected(row, context, formula)||query&&!JSON.stringify([row.path,row.note]).toLocaleLowerCase().includes(query.toLocaleLowerCase())) continue;
    const values = Object.fromEntries(requested.map(column => [column, column.startsWith('formula.') ? formula(column.slice(8))
      : column.startsWith('file.') ? safeProperty(row.file, column.slice(5)) : safeProperty(row.note, column.replace(/^note\./, ''))]));
    rows.push({ root: row.root, path: row.path, values,group:groupProperty?String(values[groupProperty]??'Unspecified'):undefined });
  }
  rows.sort((a, b) => {
    if(groupProperty&&a.group!==b.group)return a.group.localeCompare(b.group)*(view.groupBy?.direction==='DESC'?-1:1);
    for (const sort of sorting) {
      const av = a.values[sort.property], bv = b.values[sort.property];
      const difference = typeof av === 'number' && typeof bv === 'number' ? av - bv : String(av ?? '').localeCompare(String(bv ?? ''));
      if (difference) return sort.direction === 'DESC' ? -difference : difference;
    }
    return a.path.localeCompare(b.path);
  });
  const count = Math.min(rows.length, Number.isInteger(view.limit) && view.limit >= 0 ? view.limit : rows.length);
  offset = Math.max(0, Number.isInteger(offset) ? offset : 0); limit = Math.max(1, Math.min(200, Number.isInteger(limit) ? limit : 100));
  return { views: base.views.map(v => ({ name: v.name || v.type, type: v.type })), viewIndex, type: view.type,
    columns: columns.map(key => ({ key, name: base.properties?.[key]?.displayName || key.replace(/^file\.|^note\./, '') })),
    rows: rows.slice(offset, Math.min(count, offset + limit)), total: count, hasMore: offset + limit < count, invalid,
    groupProperty,definition:base,qualification:qualification||'Downloaded working files only; this is not a complete inventory of Box or the previous Obsidian vault.' };
}

export function editBaseDefinition(text,index,changes){
 yamlData(text);
 const doc=parseDocument(text,{uniqueKeys:true});if(doc.errors.length)throw Error(doc.errors[0].message);
 if(!Number.isInteger(index)||!doc.getIn(['views',index]))throw Error('Choose an existing Base view.');
 for(const [key,value]of Object.entries(changes)){if(!['type','order','sort','groupBy','filters'].includes(key))throw Error('Unsupported Base setting.');if(value===null)doc.deleteIn(['views',index,key]);else doc.setIn(['views',index,key],value);}
 return String(doc);
}
