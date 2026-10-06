import { afterEach, describe, expect, it, vi } from "vitest";
import { currentTheme, initTheme, setTheme, storedTheme } from "./theme";

function mockSystem(dark: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: dark && q.includes("dark"),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

afterEach(() => {
  localStorage.clear();
  document.documentElement.className = "";
  vi.unstubAllGlobals();
});

describe("theme", () => {
  it("follows the system until a theme is chosen", () => {
    mockSystem(true);
    expect(storedTheme()).toBeNull();
    initTheme();
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    setTheme("light");
    expect(storedTheme()).toBe("light");
    expect(currentTheme()).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("ignores invalid stored values", () => {
    mockSystem(false);
    localStorage.setItem("mail-archive.theme", "purple");
    expect(currentTheme()).toBe("light");
  });
});
