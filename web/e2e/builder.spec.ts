import { expect, test } from "@playwright/test";

test("registers WebMCP and confirms only an exact Core preview", async ({ page }) => {
  await page.addInitScript(() => {
    const tools: Record<string, { execute(input: unknown): Promise<unknown> | unknown }> = {};
    Object.defineProperty(window, "__webMCPTools", { value: tools });
    Object.defineProperty(document, "modelContext", { value: {
      registerTool: async (tool: { name: string; execute(input: unknown): Promise<unknown> | unknown }, options?: { signal?: AbortSignal }) => {
        tools[tool.name] = tool;
        options?.signal?.addEventListener("abort", () => { delete tools[tool.name]; });
      },
    } });
  });
  await page.goto("/");
  await expect.poll(() => page.evaluate(() => Object.keys((window as unknown as { __webMCPTools: object }).__webMCPTools).length)).toBe(5);

  const result = await page.evaluate(async () => {
    const tools = (window as unknown as { __webMCPTools: Record<string, { execute(input: unknown): Promise<unknown> }> }).__webMCPTools;
    const canvas = await tools.inspect_current_canvas.execute({});
    const preview = await tools.preview_workflow_admission.execute({
      job: canvas.definition.job, campaign: canvas.definition.campaign, workflow: canvas.definition.workflows[0],
    });
    return tools.confirm_authorized_action.execute({ preview: preview.preview });
  });
  expect(result.canvas.definition.workflow_states["research-review@1"]).toBe("admitted");
});

test("restores a canonical approval after page reload", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: /Recommendation, Completed/ }).click();
  await page.getByRole("button", { name: "Review exact action" }).click();
  await page.getByRole("button", { name: "Preview decision" }).click();
  await page.getByRole("button", { name: "Approve exact action" }).click();
  await page.reload();
  await expect(page.getByRole("button", { name: /Recommendation, Completed/ })).toContainText("approved");
});

test("drafts a Job and Workflow, previews, confirms, and renders canonical Canvas", async ({ page }) => {
  await page.goto("/");
  await page.evaluate(() => localStorage.clear());
  await page.reload();

  await page.getByRole("button", { name: "Build Workflow" }).click();
  await page.getByRole("textbox", { name: "Job ID" }).fill("e2e-job");
  await page.getByRole("textbox", { name: "Job title" }).fill("E2E evidence job");
  await page.getByRole("textbox", { name: "Campaign ID" }).fill("e2e-campaign");
  await page.getByRole("textbox", { name: "Workflow ID" }).fill("e2e-review");
  await page.getByRole("button", { name: "Compile & preview" }).click();

  await expect(page.getByText("Ready to admit version 1")).toBeVisible();
  await expect(page.getByText("Authorities: canonical, derived").first()).toBeVisible();
  await expect(page.getByText(/Budget: 2 attempts/)).toBeVisible();
  await expect(page.getByText(/Completion:/)).toBeVisible();
  await expect(page.getByText(/No action when:/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm admission" }).click();
  await expect(page.getByRole("button", { name: /Research and review, Admitted/ })).toContainText("e2e-review@1");
  await expect(page.getByRole("button", { name: /E2E evidence job, Configured/ })).toBeVisible();
  await page.reload();
  const jobSelector = page.getByRole("combobox", { name: "Select Job" });
  await expect(jobSelector.locator("option")).toHaveCount(2);
  await jobSelector.selectOption("example-job");
  await expect(page.getByRole("button", { name: /Example research job, Configured/ })).toBeVisible();
  await jobSelector.selectOption("e2e-job");
  await page.getByRole("button", { name: "Definition" }).click();
  await expect(page.getByRole("button", { name: /E2E evidence job, Configured/ })).toBeVisible();
});

test("keeps an admitted Campaign Workflow plan immutable", async ({ page }) => {
  await page.goto("/");
  await page.evaluate(() => localStorage.clear());
  await page.reload();

  await page.getByRole("button", { name: "Build Workflow" }).click();
  await page.getByRole("textbox", { name: "Workflow ID" }).fill("new-review");
  await page.getByRole("button", { name: "Compile & preview" }).click();
  await expect(page.getByText("Ready to admit version 1")).toBeVisible();
  await page.getByRole("button", { name: "Confirm admission" }).click();
  await expect(page.getByText("Campaign definition is immutable after its first Workflow admission")).toBeVisible();
});

test("adds and reloads a second independent Campaign", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Build Workflow" }).click();
  await page.getByRole("button", { name: "New Campaign" }).click();
  const draftSelector = page.getByRole("combobox", { name: "Draft Campaign" });
  await expect(draftSelector.locator("option")).toHaveCount(2);
  const newDraft = await draftSelector.inputValue();
  const priorDraft = await draftSelector.locator("option").evaluateAll((options, selected) => options.map((option) => (option as HTMLOptionElement).value).find((value) => value !== selected), newDraft);
  await draftSelector.selectOption(priorDraft!);
  await draftSelector.selectOption(newDraft);
  await page.getByRole("textbox", { name: "Campaign ID" }).fill("e2e-second-campaign");
  await page.getByRole("textbox", { name: "Campaign title" }).fill("Second evidence campaign");
  await page.getByRole("textbox", { name: "Workflow ID" }).fill("second-review");
  await page.getByRole("spinbutton", { name: "Campaign attempts" }).fill("7");
  await page.getByRole("button", { name: "Compile & preview" }).click();
  await expect(page.getByText("Ready to admit version 1")).toBeVisible();
  await page.getByRole("button", { name: "Confirm admission" }).click();

  const selector = page.getByRole("combobox", { name: "Select Campaign" });
  await expect(selector).toHaveValue("e2e-second-campaign");
  await expect(selector.locator("option")).toHaveCount(2);
  const previousCampaign = await selector.locator("option").evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value).find((value) => value !== "e2e-second-campaign"));
  await selector.selectOption(previousCampaign!);
  await expect(selector).toHaveValue(previousCampaign!);
  await selector.selectOption("e2e-second-campaign");
  await expect(page.getByRole("button", { name: /Second evidence campaign, Admitted/ })).toBeVisible();

  await page.reload();
  await expect(page.getByRole("combobox", { name: "Select Campaign" })).toHaveValue("e2e-second-campaign");
  await expect(page.getByRole("button", { name: /Second evidence campaign, Admitted/ })).toBeVisible();
});
