const intersects = (selection, from, to) => selection.ranges.some(range => range.from <= to && range.to >= from);

// Presentation only: Lezer owns Markdown parsing; these spans never rewrite the
// document. Limit work to the visible range, including its enclosing constructs.
export function markdownPresentation(state, tree, from, to, focused) {
  const result = [], lines = new Set();
  const text = (start, end) => state.doc.sliceString(start, end);
  const active = node => focused && intersects(state.selection, node.from, node.to);
  const mark = (start, end, className, reference) => { if (end > start) result.push({ kind: 'mark', from: start, to: end, className, reference }); };
  const hide = (start, end) => { if (end > start) result.push({ kind: 'hide', from: start, to: end }); };
  const line = (position, className) => {
    const start = state.doc.lineAt(position).from, key = `${start}:${className}`;
    if (!lines.has(key)) { result.push({ kind: 'line', from: start, className }); lines.add(key); }
  };
  const first = state.doc.line(1), frontmatter = first.text === '---' ? (() => {
    for (let i = 2; i <= Math.min(state.doc.lines, 200); i++) if (/^(---|\.\.\.)$/.test(state.doc.line(i).text)) return state.doc.line(i).to;
    return 0;
  })() : 0;
  tree.iterate({ from, to, enter(ref) {
    const node = ref.node, name = ref.type.name;
    if (frontmatter && node.from < frontmatter && name !== 'Document') {
      for (let p = state.doc.lineAt(Math.max(from, node.from)).from; p <= Math.min(node.to, to, frontmatter);) {
        line(p, 'lp-metadata'); const current = state.doc.lineAt(p); if (current.to >= state.doc.length) break; p = current.to + 1;
      }
      return false;
    }
    if (/^(ATX|Setext)Heading[1-6]$/.test(name)) line(node.from, `lp-heading lp-h${name.at(-1)}`);
    if (name === 'HeaderMark') { if (!active(node.parent)) hide(node.from, node.to + (text(node.to, node.to + 1) === ' ' ? 1 : 0)); else mark(node.from, node.to, 'lp-syntax'); }
    if (name === 'StrongEmphasis') mark(node.from, node.to, 'lp-strong');
    if (name === 'Emphasis') mark(node.from, node.to, 'lp-emphasis');
    if(name==='StrongEmphasis'||name==='Emphasis')for(let parent=node.parent;parent;parent=parent.parent)if(parent.name===(name==='Emphasis'?'StrongEmphasis':'Emphasis')){mark(node.from,node.to,'lp-strong-emphasis');break;}
    if (name === 'Strikethrough') mark(node.from, node.to, 'lp-strike');
    if (name === 'EmphasisMark' || name === 'StrikethroughMark') { if (!active(node.parent)) hide(node.from, node.to); else mark(node.from, node.to, 'lp-syntax'); }
    if (name === 'InlineCode') mark(node.from, node.to, 'lp-inline-code');
    if (name === 'CodeMark' && node.parent?.name === 'InlineCode') { if (!active(node.parent)) hide(node.from, node.to); else mark(node.from, node.to, 'lp-syntax'); }
    if (name === 'ListMark') {
      if (!active(state.doc.lineAt(node.from)) && /^[*+-]$/.test(text(node.from, node.to))) result.push({ kind: 'bullet', from: node.from, to: node.to });
      else mark(node.from, node.to, 'lp-syntax');
    }
    if (name === 'TaskMarker' && !active(node)) result.push({ kind: 'task', from: node.from, to: node.to, checked: /x/i.test(text(node.from, node.to)) });
    if (name === 'Blockquote') for (let p = state.doc.lineAt(Math.max(from, node.from)).from; p <= Math.min(node.to, to);) {
      line(p, 'lp-quote'); const current = state.doc.lineAt(p); if (current.to >= state.doc.length) break; p = current.to + 1;
    }
    if (name === 'QuoteMark' && !active(state.doc.lineAt(node.from))) hide(node.from, node.to);
    if (name === 'FencedCode' || name === 'CodeBlock') {
      for (let p = state.doc.lineAt(Math.max(from, node.from)).from; p <= Math.min(node.to, to);) {
        line(p, 'lp-code-block'); const current = state.doc.lineAt(p); if (current.to >= state.doc.length) break; p = current.to + 1;
      }
      if(name==='FencedCode'&&!active(node))for(const child of [node.getChild('CodeInfo'),...node.getChildren('CodeMark')])if(child)hide(child.from,child.to);
      return false;
    }
    if (name === 'Link' || name === 'Image') {
      const url = node.getChild('URL'), marks = node.getChildren('LinkMark');
      if (url && marks.length >= 2) {
        const start = marks[0].to, end = marks[1].from, reference = text(url.from, url.to);
        if (name === 'Image' && !active(node)) { result.push({ kind: 'image', from: node.from, to: node.to, reference, label: text(start, end) }); return false; }
        mark(start, end, 'lp-link', reference);
        if (!active(node)) { hide(node.from, start); hide(end, node.to); }
      }
    }
  } });
  for (let i = state.doc.lineAt(from).number; i <= state.doc.lineAt(to).number; i++) {
    const current = state.doc.line(i);
    if (frontmatter && current.from < frontmatter) continue;
    const syntax = tree.resolveInner(current.from + Math.min(1, current.length), 1);
    let code = false; for (let n = syntax; n; n = n.parent) if (['FencedCode', 'CodeBlock', 'InlineCode'].includes(n.name)) code = true;
    if (code) continue;
    for (const match of current.text.matchAll(/(!?)\[\[([^\]\n]+)\]\]/g)) {
      if (current.text[match.index - 1] === '\\') continue;
      const start = current.from + match.index, end = start + match[0].length;
      const ancestor = tree.resolveInner(start + 1, 1);
      let inlineCode = false; for (let n = ancestor; n; n = n.parent) if (n.name === 'InlineCode') inlineCode = true;
      if (inlineCode) continue;
      const [reference, alias] = match[2].split('|');
      if (match[1] && !(focused && intersects(state.selection, start, end))) result.push({ kind: 'image', from: start, to: end, reference:match[2], label: alias || reference });
      else {
        const labelFrom = alias ? start + match[0].indexOf('|') + 1 : start + 2 + match[1].length;
        mark(labelFrom, end - 2, 'lp-link', reference);
        if (!(focused && intersects(state.selection, start, end))) { hide(start, labelFrom); hide(end - 2, end); }
      }
    }
    for(const match of current.text.matchAll(/(^|[\s([])#([\p{L}\p{N}_][\p{L}\p{N}_/-]*)/gu)){
      const start=current.from+match.index+match[1].length,end=start+match[2].length+1;
      let literal=false;for(let node=tree.resolveInner(start+1,1);node;node=node.parent)if(['InlineCode','HTMLTag','URL'].includes(node.name))literal=true;
      if(!literal)result.push({kind:'mark',from:start,to:end,className:'lp-tag',tag:match[2]});
    }
  }
  return result;
}
