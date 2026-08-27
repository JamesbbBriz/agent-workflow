import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "web/e2e",
  workers: 1,
  use: { baseURL: "http://127.0.0.1:5173", trace: "retain-on-failure" },
  webServer: [
    { command: "npm run web:dev -- --host 127.0.0.1", url: "http://127.0.0.1:5173", reuseExistingServer: false },
    { command: "sh -c 'rm -f .agent-workflow/e2e.jsonl && go run ./cmd/agent-workflow builder --listen 127.0.0.1:4321 --ledger .agent-workflow/e2e.jsonl --canvas web/public/canvas.response.json'", url: "http://127.0.0.1:4321/v1/catalog", reuseExistingServer: false },
  ],
});
