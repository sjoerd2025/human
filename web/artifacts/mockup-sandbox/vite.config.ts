import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "path";
import runtimeErrorOverlay from "@replit/vite-plugin-runtime-error-modal";
import { mockupPreviewPlugin } from "./mockupPreviewPlugin";

// Defaults let the app build/run without Replit-style env injection
// (desktop embeds and CI both build with no PORT/BASE_PATH set).
// An ambient PORT of "0" (exported by some sandboxed shells) is treated as unset.
const rawPort =
  process.env.PORT && process.env.PORT !== "0" ? process.env.PORT : "5174";

const port = Number(rawPort);

if (Number.isNaN(port) || port <= 0) {
  throw new Error(`Invalid PORT value: "${rawPort}"`);
}

const basePath = process.env.BASE_PATH ?? "/";

// Where the daemon's /api surface lives — the same info-file address the
// desktop's /api proxy forwards to, overridable for a dev daemon elsewhere.
// The documented env format is schemeless (HUMAN_DAEMON_ADDR=host:port); the
// proxy target needs a scheme, so add the default when it is missing.
function daemonAddr(): string {
  const raw = process.env.HUMAN_DAEMON_ADDR ?? "127.0.0.1:19285";
  return raw.includes("://") ? raw : `http://${raw}`;
}

export default defineConfig({
  base: basePath,
  plugins: [
    mockupPreviewPlugin(),
    react(),
    tailwindcss(),
    runtimeErrorOverlay(),
    ...(process.env.NODE_ENV !== "production" &&
    process.env.REPL_ID !== undefined
      ? [
          await import("@replit/vite-plugin-cartographer").then((m) =>
            m.cartographer({
              root: path.resolve(import.meta.dirname, ".."),
            }),
          ),
        ]
      : []),
  ],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
    },
  },
  root: path.resolve(import.meta.dirname),
  build: {
    outDir: path.resolve(import.meta.dirname, "dist"),
    emptyOutDir: true,
  },
  server: {
    port,
    host: "0.0.0.0",
    allowedHosts: true,
    fs: {
      strict: true,
    },
    // The generated client fetches relative /api paths; outside the desktop
    // app there is no asset server to proxy them, so dev mode forwards them
    // to the daemon directly — the same surface the desktop proxies to.
    proxy: {
      "/api": {
        target: daemonAddr(),
        changeOrigin: false,
      },
    },
  },
  preview: {
    port,
    host: "0.0.0.0",
    allowedHosts: true,
  },
});
