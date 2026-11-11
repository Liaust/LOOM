import { build } from 'esbuild';
import { mkdir, readFile, writeFile,cp } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const output = path.join(root, 'dist/renderer');
const nonce = randomBytes(24).toString('base64');
await mkdir(output, { recursive: true });
await build({ entryPoints: [path.join(root, 'renderer/app.js')], outfile: path.join(output, 'app.js'),
  bundle: true, format: 'esm', platform: 'browser', target: 'chrome145',
  define: { __LOOM_EDITOR_NONCE__: JSON.stringify(nonce) } });
await build({entryPoints:[path.join(root,'renderer/theme-bootstrap.js')],outfile:path.join(output,'theme-bootstrap.js'),bundle:true,format:'iife',platform:'browser',target:'chrome145'});
await build({entryPoints:[path.join(root,'renderer/csv-worker.js')],outfile:path.join(output,'csv-worker.js'),bundle:true,format:'esm',platform:'browser',target:'chrome145'});
const html = (await readFile(path.join(root, 'renderer/index.html'), 'utf8'))
  .replaceAll('LOOM_EDITOR_STYLE_NONCE', nonce).replaceAll('../node_modules/', '../../node_modules/');
const css = (await Promise.all(['styles.css','workspace-qol.css','note-context.css','knowledge-qol.css','project-qol.css'].map(name=>readFile(path.join(root,'renderer',name),'utf8')))).join('\n').replaceAll('../assets/', '../../assets/');
await writeFile(path.join(output, 'index.html'), html);
await writeFile(path.join(output, 'styles.css'), css);
await writeFile(path.join(output,'themes.css'),await readFile(path.join(root,'renderer/themes.css'),'utf8'));
await cp(path.join(root,'node_modules/katex/dist/katex.min.css'),path.join(output,'katex.css'));
await cp(path.join(root,'node_modules/katex/dist/fonts'),path.join(output,'fonts'),{recursive:true});
const pdf=path.join(root,'node_modules/pdfjs-dist');
await cp(path.join(pdf,'build/pdf.worker.mjs'),path.join(output,'pdf.worker.mjs'));
for(const [source,destination] of [['cmaps','pdf-cmaps'],['standard_fonts','pdf-fonts'],['wasm','pdf-wasm'],['web/images','pdf-images']])
  await cp(path.join(pdf,source),path.join(output,destination),{recursive:true});
// Keep the maintained viewer's CSS isolated from the rest of Workspace.
const pdfCSS=(await readFile(path.join(pdf,'web/pdf_viewer.css'),'utf8')).replaceAll(':root',':scope').replaceAll('url(images/','url(pdf-images/');
await writeFile(path.join(output,'pdf-viewer.css'),`@scope (.loom-pdf) {\n${pdfCSS}\n}\n`);
