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
    rollupOptions: {
      output: {
        /**
         * Split only the vendor packages that are statically imported from
         * the entry (first paint) into stable chunks, so app-code iterations
         * no longer invalidate the heavy vendor hashes. Everything else in
         * node_modules (tiptap/prosemirror, @tanstack/virtual, page-only
         * deps) must keep Rollup's default chunking — assigning lazy-only
         * packages to a named chunk would pull them into the eager graph.
         */
        manualChunks(id) {
          if (!id.includes("node_modules/")) return undefined;
          const rest = id.slice(id.lastIndexOf("node_modules/") + 13);
          const parts = rest.split("/");
          const pkg = parts[0].startsWith("@")
            ? `${parts[0]}/${parts[1]}`
            : parts[0];
          if (pkg === "react" || pkg === "react-dom" || pkg === "scheduler") {
            return "vendor-react";
          }
          if (pkg === "react-router" || pkg === "react-router-dom") {
            return "vendor-router";
          }
          if (
            pkg === "@tanstack/react-query" ||
            pkg === "@tanstack/query-core"
          ) {
            return "vendor-query";
          }
          return undefined;
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
    testTimeout: 20000,
    setupFiles: ["src/test-setup.ts"],
  },
});
