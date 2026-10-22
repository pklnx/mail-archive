import { describe, expect, it } from "vitest";
import { isPlainClick } from "./clicks";

const click = (init: MouseEventInit = {}) => new MouseEvent("click", { button: 0, ...init });

describe("isPlainClick", () => {
  it("is true for a plain left click only", () => {
    expect(isPlainClick(click())).toBe(true);
  });

  it("leaves modified clicks and other buttons to the browser", () => {
    // Ctrl/Cmd: new tab; Shift: new window; Alt: download.
    for (const key of ["metaKey", "ctrlKey", "shiftKey", "altKey"] as const) {
      expect(isPlainClick(click({ [key]: true })), key).toBe(false);
    }
    // Middle and right button.
    expect(isPlainClick(click({ button: 1 }))).toBe(false);
    expect(isPlainClick(click({ button: 2 }))).toBe(false);
  });
});
