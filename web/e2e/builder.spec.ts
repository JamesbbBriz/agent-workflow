import { expect, test } from "@playwright/test";

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
  await page.getByRole("button", { name: "Definition" }).click();
  await expect(page.getByRole("button", { name: /E2E evidence job, Configured/ })).toBeVisible();
});

test("adds a new Workflow identity to an existing Job and Campaign", async ({ page }) => {
  await page.goto("/");
  await page.evaluate(() => localStorage.clear());
  await page.reload();

  await page.getByRole("button", { name: "Build Workflow" }).click();
  await page.getByRole("textbox", { name: "Workflow ID" }).fill("new-review");
  await page.getByRole("button", { name: "Compile & preview" }).click();
  await expect(page.getByText("Ready to admit version 1")).toBeVisible();
  await page.getByRole("button", { name: "Confirm admission" }).click();
  await expect(page.getByRole("button", { name: /Research and review, Admitted/ })).toContainText("new-review@1");
  await expect(page.getByRole("button", { name: /Recommendation, Completed/ })).toHaveCount(0);

  await page.reload();
  await page.getByRole("button", { name: "Definition" }).click();
  await expect(page.getByRole("button", { name: /Research and review, Admitted/ })).toContainText("new-review@1");
  await expect(page.getByRole("button", { name: /Recommendation, Completed/ })).toHaveCount(0);

  await page.getByRole("button", { name: "Build Workflow" }).click();
  await expect(page.getByRole("textbox", { name: "Workflow ID" })).toHaveValue("new-review");
});
