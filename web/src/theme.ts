// Light/dark theme: follows the system until the user picks one in the
// header; the choice is remembered in localStorage (per browser).

import { useSyncExternalStore } from "react";

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

/** The page background per theme (Carbon's white and g100), for the browser and status bar. */
const barColor: Record<Theme, string> = { light: "#ffffff", dark: "#161616" };

/** Carbon's theme class per theme. On <html>, so portaled overlays follow it too. */
export const carbonTheme: Record<Theme, string> = { light: "cds--white", dark: "cds--g100" };

/** Fired on window when a theme is applied, so every toggle on screen shows it. */
export const THEME_EVENT = "mail-archive:theme";

export function applyTheme(theme: Theme) {
  const root = document.documentElement;
  root.classList.toggle(carbonTheme.light, theme === "light");
  root.classList.toggle(carbonTheme.dark, theme === "dark");
  root.style.colorScheme = theme;
  // index.html has one theme-color per system scheme; a chosen theme wins over both.
  for (const meta of document.querySelectorAll('meta[name="theme-color"]')) meta.setAttribute("content", barColor[theme]);
  window.dispatchEvent(new Event(THEME_EVENT));
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

function subscribe(changed: () => void) {
  window.addEventListener(THEME_EVENT, changed);
  return () => window.removeEventListener(THEME_EVENT, changed);
}

/** The applied theme; a component using it follows a change by a toggle or by the system. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, currentTheme);
}
