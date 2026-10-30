import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// The dev server proxies the API to a locally running urmaild so that the
// frontend and the backend share an origin during development, which keeps
// cookies and CSRF behaving the same way as in production.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    // Acceptance pages must not reload midway when concurrent work updates source.
    hmr: process.env.URMAIL_E2E_NATIVE_FIXTURE === "1" ? false : undefined,
    port: 5173,
    proxy: {
      "/admin": {
        target: process.env.URMAIL_DEV_API ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      "/api": {
        target: process.env.URMAIL_DEV_API ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      "/metrics": {
        target: process.env.URMAIL_DEV_API ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
    testTimeout: 20000,
    setupFiles: ["src/test-setup.ts"],
  },
});
