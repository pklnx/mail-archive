import { cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { MenuItem } from "@carbon/react";
import { RowMenu, useMenuButtons, usePageFocus } from "./settingsParts";

afterEach(cleanup);

describe("useMenuButtons", () => {
  it("keeps one ref per key, so a re-render does not detach a menu", () => {
    const { result, rerender } = renderHook(() => useMenuButtons());
    const first = result.current.register("bob");
    rerender();
    expect(result.current.register("bob")).toBe(first);
    expect(result.current.register("carol")).not.toBe(first);
  });

  it("finds a registered menu's button until the menu goes", () => {
    const { result } = renderHook(() => useMenuButtons());
    const menu = document.createElement("div");
    menu.innerHTML = "<button>⋮</button>";
    result.current.register("bob")(menu);
    expect(result.current.button("bob")).toBe(menu.firstChild);
    result.current.register("bob")(null);
    expect(result.current.button("bob")).toBeNull();
  });
});

function Pages({ initial }: { initial: string | null }) {
  const [page, setPage] = useState(initial);
  const heading = useRef<HTMLHeadingElement>(null);
  usePageFocus(page, heading);
  return (
    <>
      <h1 ref={heading} tabIndex={-1}>
        {page ?? "loading"}
      </h1>
      <button onClick={() => setPage("list")}>list</button>
      <button onClick={() => setPage("detail")}>detail</button>
    </>
  );
}

describe("usePageFocus", () => {
  it("focuses the h1 on a page change, not on the first page shown", () => {
    render(<Pages initial="list" />);
    expect(document.activeElement).toBe(document.body);
    fireEvent.click(screen.getByRole("button", { name: "detail" }));
    expect(document.activeElement).toBe(screen.getByRole("heading"));
  });

  it("does not count the time before the page is decided as a page", () => {
    render(<Pages initial={null} />);
    // The first decided page is the first one shown: no focus move.
    fireEvent.click(screen.getByRole("button", { name: "list" }));
    expect(document.activeElement).toBe(document.body);
    fireEvent.click(screen.getByRole("button", { name: "detail" }));
    expect(document.activeElement).toBe(screen.getByRole("heading"));
  });
});

describe("RowMenu", () => {
  it("gets the focus back when its menu closes after a mouse click opened it", () => {
    render(
      <>
        <h1 tabIndex={-1}>page</h1>
        <RowMenu label="Actions for bob">
          <MenuItem label="Lock" onClick={() => {}} />
        </RowMenu>
      </>,
    );
    screen.getByRole("heading").focus();
    const trigger = screen.getByRole("button", { name: "Actions for bob" });
    fireEvent.click(trigger);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(document.activeElement).toBe(trigger);
  });
});
