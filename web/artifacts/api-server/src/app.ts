import express, { type Express, type Request, type Response, type NextFunction } from "express";
import { logger } from "./lib/logger";

// Dev-only bridge for standalone sandbox development (combine plan, Seam 2):
// the Go daemon now serves the /api surface itself on 127.0.0.1:19285
// (internal/daemon/webapi.go). This process exists only so `pnpm dev` in
// artifacts/mockup-sandbox works without the daemon running — it forwards
// every /api request to the daemon. No routes, no DB, no middleware beyond
// the forwarder.

const DAEMON = process.env["DAEMON_ADDR"] ?? "http://127.0.0.1:19285";

async function forward(req: Request, res: Response, next: NextFunction): Promise<void> {
  try {
    const url = `${DAEMON}${req.originalUrl}`;
    const init: RequestInit = { method: req.method, headers: { accept: "application/json" } };
    if (!["GET", "HEAD"].includes(req.method)) {
      init.body = JSON.stringify(req.body);
      (init.headers as Record<string, string>)["content-type"] = "application/json";
    }
    const upstream = await fetch(url, init);
    const body = await upstream.text();
    res.status(upstream.status).type(upstream.headers.get("content-type") ?? "application/json").send(body);
  } catch (err) {
    logger.error({ err, url: req.originalUrl }, "daemon forward failed");
    res.status(502).json({ error: `daemon unreachable at ${DAEMON}` });
  }
}

const app: Express = express();

app.all("/api/*splat", forward);

export default app;
