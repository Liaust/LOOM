const languages = new Map();
for (const [language, extensions] of Object.entries({
  markdown: 'md markdown mdown mkd mkdn mdwn',
  plaintext: 'txt text log csv tsv lock',
  restructuredtext: 'rst', asciidoc: 'adoc asciidoc',
  json: 'json jsonc jsonl ndjson geojson canvas', yaml: 'yaml yml base',
  toml: 'toml', ini: 'ini cfg conf config properties env',
  javascript: 'js mjs cjs jsx', typescript: 'ts mts cts tsx', mdx: 'mdx',
  python: 'py pyi pyw', go: 'go', rust: 'rs', java: 'java',
  kotlin: 'kt kts', scala: 'scala sc', swift: 'swift',
  c: 'c h', cpp: 'cpp cc cxx hpp hxx hh', objectivec: 'm mm',
  csharp: 'cs', fsharp: 'fs fsx', ruby: 'rb rake gemspec', php: 'php',
  r: 'r rmd', lua: 'lua', perl: 'pl pm',
  shell: 'sh bash zsh fish', powershell: 'ps1 psm1 psd1', batch: 'bat cmd',
  sql: 'sql', graphql: 'graphql gql', protobuf: 'proto', nix: 'nix',
  makefile: 'mk make', cmake: 'cmake', dockerfile: 'dockerfile',
  html: 'html htm xhtml', css: 'css scss sass less',
  xml: 'xml svg xsd xsl xslt plist', vue: 'vue', svelte: 'svelte', astro: 'astro',
  latex: 'tex sty cls bib', typst: 'typ', diff: 'diff patch',
})) for (const extension of extensions.split(' ')) languages.set(extension, language);

const namedFiles = new Map([
  ['makefile', 'makefile'], ['dockerfile', 'dockerfile'],
  ['.gitignore', 'plaintext'], ['.gitattributes', 'plaintext'], ['.gitmodules', 'ini'],
  ['.dockerignore', 'plaintext'], ['.editorconfig', 'ini'],
  ['.npmrc', 'ini'], ['.nvmrc', 'plaintext'], ['.browserslistrc', 'plaintext'],
  ['.prettierrc', 'json'], ['.eslintrc', 'json'], ['.eslintignore', 'plaintext'],
  ['.env', 'ini'], ['.bashrc', 'shell'], ['.bash_profile', 'shell'],
  ['.zshrc', 'shell'], ['.zprofile', 'shell'], ['.profile', 'shell'],
]);
const basename = relative => String(relative).split(/[\\/]/).pop().toLowerCase();
const extension = name => name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '';
const languageFor = relative => {
  const name = basename(relative);
  if (/^\.env\.(?:local|development|production|test|example|sample)(?:\.local)?$/.test(name)) return 'ini';
  return namedFiles.get(name) || languages.get(extension(name));
};

export const isEditableText = relative => languageFor(relative) !== undefined;
export const isMarkdown = relative => languageFor(relative) === 'markdown';
export const editorLanguage = relative => languageFor(relative) || 'plaintext';

export function defaultFileName(name) {
  const value = name.trim(), file = basename(value);
  return !file || file.includes('.') || ['makefile', 'dockerfile'].includes(file) ? value : `${value}.md`;
}
