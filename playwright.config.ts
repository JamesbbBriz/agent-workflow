import { defineConfig } from "@playwright/test";

const uiPort = process.env.AGENT_WORKFLOW_E2E_PORT ?? "5173";
const apiPort = process.env.AGENT_WORKFLOW_E2E_API_PORT ?? "4321";
const uiOrigin = `http://127.0.0.1:${uiPort}`;
const apiOrigin = `http://127.0.0.1:${apiPort}`;

export default defineConfig({
  testDir: "web/e2e",
  workers: 1,
  use: { baseURL: uiOrigin, trace: "retain-on-failure" },
  webServer: [
    { command: `AGENT_WORKFLOW_API_TARGET=${apiOrigin} npm run web:dev -- --host 127.0.0.1 --port ${uiPort}`, url: uiOrigin, reuseExistingServer: false },
    { command: `sh -c 'rm -f .agent-workflow/e2e.jsonl .agent-workflow/e2e-webmcp-audit.jsonl && go run ./cmd/agent-workflow builder --listen 127.0.0.1:${apiPort} --ledger .agent-workflow/e2e.jsonl --canvas web/public/canvas.response.json --web-origin ${uiOrigin} --webmcp-audit .agent-workflow/e2e-webmcp-audit.jsonl'`, url: `${apiOrigin}/v1/catalog`, reuseExistingServer: false },
  ],
});
