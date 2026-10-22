import { afterEach, describe, expect, it, vi } from "vitest";
import { THEME_EVENT, currentTheme, initTheme, setTheme, storedTheme } from "./theme";

/** A system color scheme that the test can switch, like the OS setting. */
function mockSystem(dark: boolean) {
  const system = { dark, listeners: [] as (() => void)[] };
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() {
      return system.dark && q.includes("dark");
    },
    media: q,
    addEventListener: (_: string, l: () => void) => system.listeners.push(l),
    removeEventListener: () => {},
  }));
  return {
    set(next: boolean) {
      system.dark = next;
      for (const l of system.listeners) l();
    },
  };
}

const classes = () => document.documentElement.classList;
const barColors = () => [...document.querySelectorAll('meta[name="theme-color"]')].map((m) => m.getAttribute("content"));

function expectTheme(theme: "light" | "dark") {
  // Tailwind's class for the unconverted markup, and Carbon's theme on <html>.
  expect(classes().contains("dark")).toBe(theme === "dark");
  expect(classes().contains("cds--g100")).toBe(theme === "dark");
  expect(classes().contains("cds--white")).toBe(theme === "light");
  expect(document.documentElement.style.colorScheme).toBe(theme);
  for (const c of barColors()) expect(c).toBe(theme === "dark" ? "#161616" : "#ffffff");
}

afterEach(() => {
  localStorage.clear();
  document.documentElement.className = "";
  document.head.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("theme", () => {
  it("follows the system until a theme is chosen", () => {
    document.head.innerHTML = '<meta name="theme-color" content="#ffffff"><meta name="theme-color" content="#161616">';
    const system = mockSystem(true);
    expect(storedTheme()).toBeNull();
    initTheme();
    expectTheme("dark");

    // The system switches: the page follows, as long as nothing is chosen.
    system.set(false);
    expectTheme("light");
    system.set(true);
    expectTheme("dark");

    setTheme("light");
    expect(storedTheme()).toBe("light");
    expect(currentTheme()).toBe("light");
    expectTheme("light");

    // A chosen theme wins over the system from then on.
    system.set(false);
    system.set(true);
    expectTheme("light");
  });

  it("tells every toggle when it changes", () => {
    mockSystem(false);
    const seen = vi.fn();
    window.addEventListener(THEME_EVENT, seen);
    setTheme("dark");
    window.removeEventListener(THEME_EVENT, seen);
    expect(seen).toHaveBeenCalledTimes(1);
    expectTheme("dark");
  });

  it("ignores invalid stored values", () => {
    mockSystem(false);
    localStorage.setItem("mail-archive.theme", "purple");
    expect(currentTheme()).toBe("light");
  });
});
