import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join } from "node:path";

const roots = {
  auth: join(process.cwd(), "services/auth/frontend/generated"),
  tissues: join(process.cwd(), "services/tissues/frontend/generated"),
};
const types = { ".css": "text/css", ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".svg": "image/svg+xml" };

async function sendFile(response, root, relative, replacements = {}) {
  try {
    let body = await readFile(join(root, relative));
    if (Object.keys(replacements).length) {
      let text = body.toString();
      for (const [from, to] of Object.entries(replacements)) text = text.replaceAll(from, to);
      body = Buffer.from(text);
    }
    response.writeHead(200, { "Content-Type": types[extname(relative)] || "application/octet-stream" });
    response.end(body);
  } catch {
    response.writeHead(404).end("not found");
  }
}

const server = createServer(async (request, response) => {
  const pathname = new URL(request.url || "/", "http://127.0.0.1").pathname;
  if (pathname === "/auth/login" || pathname === "/auth/login/") return sendFile(response, roots.auth, "index.html");
  if (pathname.startsWith("/auth/login/")) return sendFile(response, roots.auth, pathname.slice("/auth/login/".length));
  if (pathname.startsWith("/tissues/")) return sendFile(response, roots.tissues, pathname.slice("/tissues/".length));
  if (pathname === "/") return sendFile(response, roots.tissues, "index.html", {
    "__TISSUES_AUTH_ENABLED__": "false",
    "__TISSUES_AUTHOR__": "",
  });
  response.writeHead(404).end("not found");
});

server.listen(4173, "127.0.0.1");
