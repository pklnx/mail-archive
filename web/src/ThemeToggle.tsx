import { useState } from "react";
import { currentTheme, setTheme, type Theme } from "./theme";
import { t } from "./i18n";

const icon = { width: 18, height: 18, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 1.75, strokeLinecap: "round" as const, strokeLinejoin: "round" as const, "aria-hidden": true };

/** Switches between light and dark; the choice overrides the system setting. */
export function ThemeToggle() {
  const [theme, set] = useState<Theme>(currentTheme);
  const next: Theme = theme === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      aria-label={t.switchTheme(next)}
      title={t.switchTheme(next)}
      className="shrink-0 rounded-md p-1.5 text-zinc-500 hover:bg-zinc-200 hover:text-zinc-800 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
      onClick={() => {
        setTheme(next);
        set(next);
      }}
    >
      {theme === "dark" ? (
        <svg {...icon}>
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      ) : (
        <svg {...icon}>
          <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
        </svg>
      )}
    </button>
  );
}
