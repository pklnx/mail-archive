import { act, cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import markDark from "./brand/mail-archive-mark-dark.svg";
import markLight from "./brand/mail-archive-mark-light.svg";
import { ProductMark } from "./ProductMark";
import { setTheme } from "./theme";

afterEach(() => {
  cleanup();
  localStorage.clear();
  document.documentElement.className = "";
});

describe("ProductMark", () => {
  it("shows the mark for the applied theme and follows a theme change", () => {
    const { container } = render(<ProductMark className="extra" size={40} />);
    const img = () => container.querySelector("img")!;
    // No stored choice and a light system (test/setup.ts): the light mark.
    expect(img().getAttribute("src")).toBe(markLight);
    expect(img().getAttribute("alt")).toBe("");
    expect(img().className).toBe("app-mark app-mark--light extra");
    expect(img().getAttribute("width")).toBe("40");

    act(() => setTheme("dark"));
    expect(img().getAttribute("src")).toBe(markDark);
    expect(img().className).toBe("app-mark app-mark--dark extra");

    act(() => setTheme("light"));
    expect(img().getAttribute("src")).toBe(markLight);
    expect(img().className).toBe("app-mark app-mark--light extra");
  });

  it("uses two different files for the two themes", () => {
    expect(markDark).not.toBe(markLight);
  });
});
