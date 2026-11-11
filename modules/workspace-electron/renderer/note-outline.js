import { parser, GFM } from '@lezer/markdown';

const markdown = parser.configure(GFM);

// Source offsets stay exact, including BOM/CRLF, for the owning editor to use.
export function noteOutline(text = '') {
  if (typeof text !== 'string' || !text) return [];
  const opening = /^(?:\uFEFF)?---[ \t]*\r?\n/.exec(text);
  const closing = opening && /^(?:---|\.\.\.)[ \t]*(?:\r?\n|$)/m.exec(text.slice(opening[0].length));
  const offset = opening ? closing ? opening[0].length + closing.index + closing[0].length : text.length : text.charCodeAt(0) === 0xFEFF ? 1 : 0;
  const body = text.slice(offset), result = [];
  let position = 0, line = 1;
  markdown.parse(body).iterate({ enter(ref) {
    if (ref.name === 'FencedCode' || ref.name === 'CodeBlock') return false;
    if (!/^(?:ATX|Setext)Heading[1-6]$/.test(ref.name)) return;
    const node = ref.node, omitted = [];
    node.toTree().iterate({ enter(child) {
      // Lezer recognizes the inner [label] of a wiki link as a shortcut link.
      // Keep those brackets intact until the wiki label is extracted below.
      if (child.name === 'Link' && body[child.from + node.from - 1] === '[' && body[child.to + node.from] === ']') return false;
      if (/^(?:HeaderMark|EmphasisMark|StrikethroughMark|CodeMark|LinkMark|LinkTitle|HTMLTag)$/.test(child.name) || child.name === 'URL' && ['Link', 'Image'].includes(child.node.parent?.name)) omitted.push([child.from + node.from, child.to + node.from]);
    } });
    omitted.sort((a, b) => a[0] - b[0]);
    let label = '', from = node.from;
    for (const [start, end] of omitted) { label += body.slice(from, Math.max(from, start)); from = Math.max(from, end); }
    label += body.slice(from, node.to);
    label = label.replace(/!?\[\[([^\]\n]+)\]\]/g, (_, value) => value.split('|').at(-1)).replace(/\\([!"#$%&'()*+,\-./:;<=>?@[\]\\^_`{|}~])/g, '$1').replace(/\s+/g, ' ').trim();
    const absolute = offset + node.from;
    while (position < absolute) if (text[position++] === '\n') line++;
    result.push({ text: label, level: Number(ref.name.at(-1)), line, from: absolute, to: offset + node.to - (body[node.to - 1] === '\r' ? 1 : 0) });
    return false;
  } });
  return result;
}
