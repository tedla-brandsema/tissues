import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "/auth/login/",
  plugins: [react(), tailwindcss()],
  build: { outDir: "generated", emptyOutDir: true, assetsDir: "assets", sourcemap: false },
  test: { environment: "jsdom", setupFiles: "./src/setup-tests.ts", css: true },
});
