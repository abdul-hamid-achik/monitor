import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The renderer is a static bundle loaded from file:// by the main process,
// so asset URLs are relative and nothing is inlined that the CSP
// (renderer/index.html) would block.
export default defineConfig({
  root: "renderer",
  base: "./",
  plugins: [react()],
  build: {
    outDir: "../dist/renderer",
    emptyOutDir: true,
    target: "chrome140",
    assetsInlineLimit: 0,
    sourcemap: true,
  },
});
