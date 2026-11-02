import { IconButton } from "@carbon/react";
import { Asleep, Light } from "@carbon/icons-react";
import { setTheme, useTheme, type Theme } from "./theme";
import { t } from "./i18n";

/** The icon for switching to next: the sun for light, the moon for dark. */
export const themeIcon = (next: Theme) => (next === "light" ? Light : Asleep);

/**
 * Switches between light and dark; the choice overrides the system setting.
 * A ghost icon button on the login pages; in the app the header has the
 * switch (UserPanel.tsx).
 */
export function ThemeToggle() {
  const theme = useTheme();
  const next: Theme = theme === "dark" ? "light" : "dark";
  const label = t.switchTheme(next);
  const Icon = themeIcon(next);
  return (
    <IconButton kind="ghost" label={label} align="bottom-end" onClick={() => setTheme(next)}>
      <Icon size={20} />
    </IconButton>
  );
}
