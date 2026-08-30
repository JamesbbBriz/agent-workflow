import { defineConfig } from "vitest/config";
import { loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => ({
  root: "web",
  plugins: [react(), tailwindcss()],
  build: { copyPublicDir: false },
  server: { proxy: { "/v1": loadEnv(mode, ".", "").AGENT_WORKFLOW_API_TARGET || "http://127.0.0.1:4321", "/v2": loadEnv(mode, ".", "").AGENT_WORKFLOW_API_TARGET || "http://127.0.0.1:4321" } },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
}));
