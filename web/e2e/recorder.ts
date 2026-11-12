import { createServer, type IncomingHttpHeaders } from "node:http";
import type { BrowserContext } from "@playwright/test";
import { referrerOrigin } from "./demo";

// The server on referrerOrigin that records each request's headers, for
// referrer.spec.ts and css-images.spec.ts: the demo's checks (tools/demo,
// referrerCheck, hintCheck, cssCheck and cssSVGCheck) point their links and
// images at it.

/** A 1×1 transparent GIF. */
const gif = Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64");

/**
 * Starts the recording server and lets context reach it past the guards'
 * block of other hosts. requested(path) settles with the headers of the
 * first request for path; headers(path) returns them if one came.
 */
export async function recorder(context: BrowserContext) {
  const seen = new Map<string, IncomingHttpHeaders>();
  const waiting = new Map<string, (h: IncomingHttpHeaders) => void>();
  const server = createServer((req, res) => {
    const path = new URL(req.url ?? "/", referrerOrigin).pathname;
    if (!seen.has(path)) {
      seen.set(path, req.headers);
      waiting.get(path)?.(req.headers);
    }
    if (path.startsWith("/css-")) {
      // CORS for the cors-mode ones (masks, shape-outside), so the
      // browser logs no error for an image it loads.
      res.writeHead(200, { "content-type": "image/gif", "access-control-allow-origin": "*" }).end(gif);
    } else if (/-img$/.test(path)) {
      res.writeHead(200, { "content-type": "image/gif" }).end(gif);
    } else {
      res.writeHead(200, { "content-type": "text/html" }).end("<p>check</p>");
    }
  });
  const { hostname, port } = new URL(referrerOrigin);
  await new Promise<void>((resolve, reject) => server.once("error", reject).listen(Number(port), hostname, resolve));
  // Past the guards' block of other hosts: this one is ours.
  await context.route(`${referrerOrigin}/**`, (route) => route.continue());
  return {
    requested: (path: string) =>
      new Promise<IncomingHttpHeaders>((resolve) => {
        const h = seen.get(path);
        if (h) resolve(h);
        else waiting.set(path, resolve);
      }),
    headers: (path: string) => seen.get(path),
    close: () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(resolve);
      }),
  };
}
