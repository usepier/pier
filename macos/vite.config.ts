/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { pierApiBridge } from "./dev/api-bridge.ts";

// Tauri expects a fixed dev port and serves the built files from dist/.
export default defineConfig({
  plugins: [react(), tailwindcss(), pierApiBridge()],
  clearScreen: false,
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: { port: 1420, strictPort: true, watch: { ignored: ["**/src-tauri/**"] } },
  envPrefix: ["VITE_", "TAURI_ENV_"],
  build: {
    // WKWebView on macOS 14 is Safari 17.
    target: "safari17",
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
  },
  test: { environment: "node", include: ["src/**/*.test.ts"] },
});
