import { defineConfig } from "vite";
export default defineConfig({
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:4837",
      "/healthz": "http://127.0.0.1:4837",
    },
  },
});
