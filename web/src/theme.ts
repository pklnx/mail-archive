// Light/dark theme: follows the system until the user picks one in the
// header; the choice is remembered in localStorage (per browser).

export type Theme = "light" | "dark";
const KEY = "mail-archive.theme";
const media = () => window.matchMedia("(prefers-color-scheme: dark)");

export function storedTheme(): Theme | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : null;
  } catch {
    return null; // storage blocked
  }
}

export function currentTheme(): Theme {
  return storedTheme() ?? (media().matches ? "dark" : "light");
}

/** The page background per theme (--color-bg), for the browser and status bar. */
const barColor: Record<Theme, string> = { light: "#ffffff", dark: "#09090b" };

export function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  document.documentElement.style.colorScheme = theme;
  // index.html has one theme-color per system scheme; a chosen theme wins over both.
  for (const meta of document.querySelectorAll('meta[name="theme-color"]')) meta.setAttribute("content", barColor[theme]);
}

export function setTheme(theme: Theme) {
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // not persisted; still applied for this page
  }
  applyTheme(theme);
}

/** Applies the initial theme and keeps following the system if unset. */
export function initTheme() {
  applyTheme(currentTheme());
  media().addEventListener("change", () => {
    if (!storedTheme()) applyTheme(currentTheme());
  });
}
