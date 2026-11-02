/**
 * Where an open filter menu goes (#486). The Log's Aktion and Wer menus are
 * as wide as their longest entry, so one can be wider than its field. It
 * grows from the field's start ("start") while it ends inside the bounds;
 * otherwise from the field's end to the left ("end"). When neither side has
 * room, it takes the larger side and `cap` is the width it may use there, so
 * no entry leaves the window; otherwise `cap` is null.
 *
 * `boundLeft` and `boundRight` are the edges of the visible area (the page's
 * scroll container or the window, whichever is narrower), `gutter` the space
 * kept free at each edge.
 */
export interface MenuPlacement {
  side: "start" | "end";
  cap: number | null;
}

export function menuSide(fieldLeft: number, fieldRight: number, menuWidth: number, boundLeft: number, boundRight: number, gutter = 16): MenuPlacement {
  const startRoom = boundRight - gutter - fieldLeft;
  const endRoom = fieldRight - (boundLeft + gutter);
  if (menuWidth <= startRoom) return { side: "start", cap: null };
  if (menuWidth <= endRoom) return { side: "end", cap: null };
  // Neither fits: the larger side, never narrower than the field.
  const field = fieldRight - fieldLeft;
  return startRoom >= endRoom ? { side: "start", cap: Math.max(startRoom, field) } : { side: "end", cap: Math.max(endRoom, field) };
}
