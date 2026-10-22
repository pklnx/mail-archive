import { unique } from "./demo";
import { expect, test } from "./fixtures";

// The demo's personal / Archive/2014 holds three twelve-year-old messages.
// No other test uses that folder, so deleting from it changes nothing else.
test("retention rule, legal hold, deletion run confirmed by count", async ({ page }) => {
  const note = unique("e2e rule");
  const hold = unique("e2e hold");
  await page.goto("/?view=retention");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { name: "Retention", level: 1 })).toBeVisible();
  await expect(main.getByRole("tab", { name: "Rules" })).toHaveAttribute("aria-selected", "true");

  // A ten-year rule for the old folder.
  await main.getByRole("combobox", { name: "Account", exact: true }).selectOption({ label: "demo / personal" });
  await main.getByRole("combobox", { name: "Folder", exact: true }).selectOption("Archive/2014");
  await main.getByLabel("Years").fill("10");
  await main.getByLabel("Note").fill(note);
  await main.getByRole("button", { name: "Add rule" }).click();
  const rule = main.getByRole("listitem").filter({ hasText: note });
  await expect(rule).toContainText("demo / personal / Archive/2014");
  await expect(rule).toContainText("10 years");

  // A hold for the court mail in it.
  await main.getByRole("tab", { name: "Legal holds" }).click();
  await main.getByLabel("Name", { exact: true }).fill(hold);
  await main.getByLabel("Reason").fill("Case 4711");
  await main.getByRole("combobox", { name: "Account", exact: true }).selectOption({ label: "demo / personal" });
  await main.getByLabel("Search filter").fill("from:registry@court.example");
  await main.getByRole("button", { name: "Add hold" }).click();
  await expect(main.getByText("Hold added; the filter matched 1 message(s).")).toBeVisible();
  const held = main.getByRole("listitem").filter({ hasText: hold });
  await expect(held).toContainText("account personal (demo)");
  await expect(held).toContainText("from:registry@court.example (1 matched)");

  // The run lists the two other messages, not the held one.
  await main.getByRole("tab", { name: "Deletion runs" }).click();
  await main.getByRole("button", { name: "New deletion run" }).click();
  const confirm = main.getByLabel("To delete, type the number of messages (2):");
  await expect(confirm).toBeVisible();
  await expect(main.getByRole("cell", { name: "demo / personal / Archive/2014" })).toBeVisible();
  const remove = main.getByRole("button", { name: "Delete", exact: true });
  await expect(remove).toBeDisabled();
  // Only the exact count enables the button.
  await confirm.fill("3");
  await expect(remove).toBeDisabled();
  await confirm.fill("2");
  await expect(remove).toBeEnabled();
  await remove.click();

  // The retention job deletes in the background; the page polls.
  const run = main.getByRole("listitem").filter({ hasText: /^Run \d+: 2 message\(s\)/ });
  await expect(run).toContainText("done", { timeout: 20_000 });
  await expect(run).toContainText("2 deleted, 0 kept");
  await expect(main.getByRole("button", { name: "New deletion run" })).toBeVisible();

  // The held message is still in the archive, the others are gone.
  await page.goto("/?q=" + encodeURIComponent("hearing date"));
  await expect(page.getByRole("list", { name: "Messages" }).getByRole("button", { name: /Case 4711: hearing date/ })).toBeVisible();
  await page.goto("/?q=" + encodeURIComponent("tax assessment"));
  await expect(page.getByText("No messages match your search.")).toBeVisible();
});
