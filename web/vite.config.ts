import { defineConfig, loadEnv } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Static SPA with three pages sharing src/lib: the desktop UI at / (index.html),
// the mobile UI at /m/ (m/index.html) and standalone document pages at /doc/<ref>
// (doc/index.html). In dev, proxy /api to the running
// server so the browser stays same-origin (no CORS). Override the target with
// VITE_API_BASE.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");
  const apiBase = env.VITE_API_BASE || "http://127.0.0.1:8890";
  return {
    plugins: [svelte()],
    build: {
      outDir: "dist",
      emptyOutDir: true,
      rollupOptions: {
        input: {
          desktop: "index.html", // relative to the project root (web/)
          mobile: "m/index.html",
          doc: "doc/index.html",
        },
      },
    },
    server: {
      port: 5173,
      proxy: {
        "/api": { target: apiBase, changeOrigin: true, ws: true },
      },
    },
  };
});
