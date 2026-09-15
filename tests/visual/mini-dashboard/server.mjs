import { createReadStream, existsSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const testRoot = dirname(fileURLToPath(import.meta.url));
const webRoot = normalize(join(testRoot, "../../../internal/minidashboard/web"));
const contentTypes = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".woff2": "font/woff2",
  ".txt": "text/plain; charset=utf-8",
};

createServer((request, response) => {
  const pathname = new URL(request.url || "/", "http://127.0.0.1").pathname;
  const relative = pathname === "/" ? "index.html" : pathname.slice(1);
  const target = normalize(join(webRoot, relative));
  if (!target.startsWith(`${webRoot}/`)) {
    response.writeHead(403).end();
    return;
  }
  if (!existsSync(target)) {
    response.writeHead(404).end();
    return;
  }
  response.writeHead(200, { "content-type": contentTypes[extname(target)] || "application/octet-stream", "cache-control": "no-store" });
  createReadStream(target).pipe(response);
}).listen(4173, "127.0.0.1");
