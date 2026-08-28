import { defineConfig } from "vitest/config";
import type { Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const stripTrailingWhitespace: Plugin = {
  name: "strip-trailing-whitespace",
  generateBundle(_options, bundle) {
    for (const output of Object.values(bundle)) {
      if (output.type === "chunk") output.code = output.code.replace(/[ \t]+$/gm, "");
    }
  },
};

export default defineConfig({
  base: "/tissues/",
  plugins: [react(), tailwindcss(), stripTrailingWhitespace],
  build: { outDir: "generated", emptyOutDir: true, assetsDir: "assets", sourcemap: false },
  test: { environment: "jsdom", setupFiles: "./src/setup-tests.ts", css: true },
});
