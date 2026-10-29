import { IconButton } from "@carbon/react";
import { Asleep, Light } from "@carbon/icons-react";
import { setTheme, useTheme, type Theme } from "./theme";
import { t } from "./i18n";

/**
 * Switches between light and dark; the choice overrides the system setting.
 * A ghost icon button on the login pages; in the app the user panel has
 * the switch (UserPanel.tsx).
 */
export function ThemeToggle() {
  const theme = useTheme();
  const next: Theme = theme === "dark" ? "light" : "dark";
  const label = t.switchTheme(next);
  const icon = theme === "dark" ? <Light size={20} /> : <Asleep size={20} />;
  return (
    <IconButton kind="ghost" label={label} align="bottom-end" onClick={() => setTheme(next)}>
      {icon}
    </IconButton>
  );
}
