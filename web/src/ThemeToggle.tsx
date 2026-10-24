import { HeaderGlobalAction, IconButton } from "@carbon/react";
import { Asleep, Light } from "@carbon/icons-react";
import { setTheme, useTheme, type Theme } from "./theme";
import { t } from "./i18n";

/**
 * Switches between light and dark; the choice overrides the system setting.
 * In the shell's header as a header action, elsewhere as a ghost icon button.
 */
export function ThemeToggle({ header = false }: { header?: boolean }) {
  const theme = useTheme();
  const next: Theme = theme === "dark" ? "light" : "dark";
  const label = t.switchTheme(next);
  const icon = theme === "dark" ? <Light size={20} /> : <Asleep size={20} />;
  const toggle = () => setTheme(next);
  if (header) {
    return (
      <HeaderGlobalAction aria-label={label} tooltipAlignment="end" onClick={toggle}>
        {icon}
      </HeaderGlobalAction>
    );
  }
  return (
    <IconButton kind="ghost" label={label} align="bottom-end" onClick={toggle}>
      {icon}
    </IconButton>
  );
}
