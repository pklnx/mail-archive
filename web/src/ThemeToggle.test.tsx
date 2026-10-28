import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeToggle } from "./ThemeToggle";
import { t } from "./i18n";

afterEach(() => {
  cleanup();
  localStorage.clear();
  document.documentElement.className = "";
});

describe("ThemeToggle", () => {
  it("switches the theme, and every toggle on screen follows", () => {
    // Two on screen: each follows the theme (theme.ts), whichever was used.
    render(
      <>
        <ThemeToggle />
        <ThemeToggle />
      </>,
    );
    expect(screen.getAllByRole("button", { name: t.switchTheme("dark") })).toHaveLength(2);

    fireEvent.click(screen.getAllByRole("button", { name: t.switchTheme("dark") })[0]!);
    expect(document.documentElement.classList.contains("cds--g100")).toBe(true);
    expect(screen.getAllByRole("button", { name: t.switchTheme("light") })).toHaveLength(2);

    fireEvent.click(screen.getAllByRole("button", { name: t.switchTheme("light") })[1]!);
    expect(document.documentElement.classList.contains("cds--white")).toBe(true);
    expect(screen.getAllByRole("button", { name: t.switchTheme("dark") })).toHaveLength(2);
  });
});
