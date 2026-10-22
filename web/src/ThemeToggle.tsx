import { useSyncExternalStore } from "react";
import { HeaderGlobalAction, IconButton } from "@carbon/react";
import { Asleep, Light } from "@carbon/icons-react";
import { THEME_EVENT, currentTheme, setTheme, type Theme } from "./theme";
import { t } from "./i18n";

function subscribe(changed: () => void) {
  window.addEventListener(THEME_EVENT, changed);
  return () => window.removeEventListener(THEME_EVENT, changed);
}

/** The applied theme; every toggle on screen follows a change by another one or by the system. */
function useTheme(): Theme {
  return useSyncExternalStore(subscribe, currentTheme);
}

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
