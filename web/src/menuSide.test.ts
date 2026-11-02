// Where an open Log filter menu goes (#486).
import { describe, expect, it } from "vitest";
import { menuSide } from "./menuSide";

// A window of 1000 px, a field of 200 px from 600 to 800.
const left = 0;
const right = 1000;

describe("menuSide", () => {
  it("grows from the field's start when the menu ends inside the window", () => {
    expect(menuSide(600, 800, 300, left, right)).toEqual({ side: "start", cap: null });
    // Exactly at the gutter still fits.
    expect(menuSide(600, 800, 384, left, right)).toEqual({ side: "start", cap: null });
  });

  it("grows to the left from the field's end one pixel past the gutter", () => {
    expect(menuSide(600, 800, 385, left, right)).toEqual({ side: "end", cap: null });
  });

  it("keeps the start for a field at the window's start with a menu as wide as the window allows", () => {
    expect(menuSide(16, 216, 1000 - 32, left, right)).toEqual({ side: "start", cap: null });
  });

  it("keeps the start for a menu narrower than the field", () => {
    expect(menuSide(600, 800, 150, left, right)).toEqual({ side: "start", cap: null });
  });

  it("measures against the visible area, not the window, when that is narrower", () => {
    // A scroll container from 256 to 900 (a sidebar before it, a scrollbar after it).
    expect(menuSide(600, 800, 285, 256, 900)).toEqual({ side: "end", cap: null });
    expect(menuSide(600, 800, 284, 256, 900)).toEqual({ side: "start", cap: null });
    // The end side ends at the container's start plus the gutter.
    expect(menuSide(600, 800, 528, 256, 900)).toEqual({ side: "end", cap: null });
    expect(menuSide(600, 800, 529, 256, 900)).toEqual({ side: "end", cap: 528 });
  });

  it("caps the menu to the larger side when neither has room", () => {
    // Start room 1000 - 16 - 300 = 684, end room 500 - 16 = 484.
    expect(menuSide(300, 500, 900, left, right)).toEqual({ side: "start", cap: 684 });
    // Start room 1000 - 16 - 500 = 484, end room 700 - 16 = 684.
    expect(menuSide(500, 700, 900, left, right)).toEqual({ side: "end", cap: 684 });
  });

  it("never caps the menu below the field's width", () => {
    // A field wider than the room on either side (it overflows the bounds itself).
    expect(menuSide(-50, 1050, 1200, left, right)).toEqual({ side: "start", cap: 1100 });
  });
});
