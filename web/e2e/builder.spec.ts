import { expect, test } from "@playwright/test";

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
  await expect(page.getByText(/Budget: 2 attempts/)).toBeVisible();
  await expect(page.getByText(/Completion:/)).toBeVisible();
  await expect(page.getByText(/No action when:/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm admission" }).click();
  await expect(page.getByRole("button", { name: /Research and review, Admitted/ })).toContainText("e2e-review@1");
  await expect(page.getByRole("button", { name: /E2E evidence job, Configured/ })).toBeVisible();
});
