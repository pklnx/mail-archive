import { origin, unique } from "./demo";
import { expect, test } from "./fixtures";

test("add a user, copy the generated password, lock and unlock", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const name = unique("e2e-user");
  await page.goto("/?view=users");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Users", level: 1 })).toBeVisible();

  // Add user opens a Modal with the name and the admin choice.
  await main.getByRole("button", { name: "Add user" }).click();
  const add = page.getByRole("dialog", { name: "Add user" });
  await add.getByLabel("User name").fill(name);
  await expect(add.getByRole("checkbox", { name: "Admin" })).not.toBeChecked();
  await add.getByRole("button", { name: "Add user" }).click();
  await expect(add).toHaveCount(0);

  // The password shows once, with a copy button.
  const generated = main.getByRole("status").filter({ hasText: `Password for ${name}` });
  await expect(generated).toBeVisible();
  const password = (await generated.getByRole("code").textContent())?.trim() ?? "";
  expect(password.length).toBeGreaterThanOrEqual(12);
  await generated.getByRole("button", { name: "Copy to clipboard" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(password);
  await generated.getByRole("button", { name: "Done" }).click();
  await expect(generated).toHaveCount(0);

  const user = main.getByRole("row").filter({ has: page.getByText(name, { exact: true }) });
  await expect(user).toContainText("must change password");

  // Locking asks first; unlocking does not.
  await user.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Lock" }).click();
  const confirm = page.getByRole("dialog", { name: `Lock “${name}”?` });
  await expect(confirm).toBeVisible();
  await expect(user.getByText("locked", { exact: true })).toHaveCount(0);
  await confirm.getByRole("button", { name: "Lock" }).click();
  await expect(user.getByText("locked", { exact: true })).toBeVisible();
  await user.getByRole("button", { name: `Actions for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Unlock" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(user.getByText("locked", { exact: true })).toHaveCount(0);
  await expect(user).toContainText("must change password");
});
