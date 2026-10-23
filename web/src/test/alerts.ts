import { screen, waitFor } from "@testing-library/react";

/**
 * The one alert that says something. Carbon's TextInput keeps an empty
 * role="alert" live region (for its character counter) in every text
 * field; it stays empty and is not one of the app's alerts. More than one
 * alert with text fails, as getByRole would.
 */
export function theAlert(): HTMLElement {
  const alerts = screen.queryAllByRole("alert").filter((a) => a.textContent !== "");
  if (alerts.length !== 1) throw new Error(`expected one alert with text, found ${alerts.length}`);
  return alerts[0]!;
}

/** Waits for theAlert(). */
export function findAlert(): Promise<HTMLElement> {
  return waitFor(theAlert);
}
