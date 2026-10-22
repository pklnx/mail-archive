import { origin, unique } from "./demo";
import { acceptDialog, expect, test } from "./fixtures";

test("add a user, copy the generated password, lock and unlock", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const name = unique("e2e-user");
  await page.goto("/?view=users");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeVisible();

  await main.getByRole("button", { name: "Add user" }).click();
  await main.getByLabel("User name").fill(name);
  await expect(main.getByRole("checkbox", { name: "Admin" })).not.toBeChecked();
  await main.getByRole("button", { name: "Add user" }).click();

  // The password shows once, with a copy button.
  const generated = main.getByRole("status").filter({ hasText: `Password for ${name}` });
  await expect(generated).toBeVisible();
  const password = (await generated.getByRole("code").textContent())?.trim() ?? "";
  expect(password.length).toBeGreaterThanOrEqual(12);
  await generated.getByRole("button", { name: "Copy" }).click();
  await expect(generated.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(password);
  await generated.getByRole("button", { name: "Done" }).click();
  await expect(generated).toHaveCount(0);

  const user = main.getByRole("listitem").filter({ has: page.getByText(name, { exact: true }) });
  await expect(user).toContainText("must change password");

  // Locking asks first; unlocking does not.
  acceptDialog(page);
  await user.getByRole("button", { name: "Lock" }).click();
  await expect(user.getByText("locked", { exact: true })).toBeVisible();
  await user.getByRole("button", { name: "Unlock" }).click();
  await expect(user.getByRole("button", { name: "Lock" })).toBeVisible();
  await expect(user.getByText("locked", { exact: true })).toHaveCount(0);
  await expect(user).toContainText("must change password");
});
