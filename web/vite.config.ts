import { resolve } from "node:path";
import { defineConfig } from "vitest/config";

// Two entries: the viewer at / and the admin UI at /admin/. The proxy keeps
// the Host header, so URLs the server builds from it, such as the single
// sign-on callback, point back at this dev server.
const api = process.env.RABBIT_EARS_API ?? "http://localhost:8477";
export default defineConfig({
  server: { port: 5173, proxy: { "/api": { target: api, changeOrigin: false } } },
  build: {
    // Raised for hls.js, which loads on demand.
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      input: {
        viewer: resolve(import.meta.dirname, "index.html"),
        admin: resolve(import.meta.dirname, "admin/index.html"),
      },
    },
  },
  test: { environment: "jsdom" },
});
