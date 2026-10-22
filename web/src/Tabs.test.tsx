import { cleanup, createEvent, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Tabs } from "./Tabs";

type Id = "one" | "two" | "three";

function Harness({ onSelect }: { onSelect: (id: Id) => void }) {
  const [selected, setSelected] = useState<Id>("one");
  return (
    <Tabs
      label="Sections"
      selected={selected}
      onSelect={(id) => {
        onSelect(id);
        setSelected(id);
      }}
      tabs={[
        { id: "one", label: "One", panel: <p>Panel one</p> },
        { id: "two", label: "Two", panel: <input aria-label="Field two" /> },
        { id: "three", label: "Three", panel: <p>Panel three</p> },
      ]}
    />
  );
}

const tab = (name: string) => screen.getByRole("tab", { name });

function setup() {
  const onSelect = vi.fn();
  render(<Harness onSelect={onSelect} />);
  return onSelect;
}

afterEach(cleanup);

describe("Tabs", () => {
  it("renders a tablist with linked tabs and panels", () => {
    setup();
    expect(screen.getByRole("tablist", { name: "Sections" })).toBeTruthy();
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((t) => t.textContent)).toEqual(["One", "Two", "Three"]);
    for (const t of tabs) {
      const panel = document.getElementById(t.getAttribute("aria-controls")!)!;
      expect(panel.getAttribute("role")).toBe("tabpanel");
      expect(panel.getAttribute("aria-labelledby")).toBe(t.id);
    }
    expect(tabs.map((t) => t.getAttribute("aria-selected"))).toEqual(["true", "false", "false"]);
    expect(tabs.map((t) => t.tabIndex)).toEqual([0, -1, -1]);
    expect(screen.getByRole("tabpanel", { name: "One" }).tabIndex).toBe(0);
  });

  it("shows one panel at a time and keeps the others mounted", () => {
    setup();
    const shown = screen.getAllByRole("tabpanel");
    expect(shown).toHaveLength(1);
    expect(shown[0]!.textContent).toBe("Panel one");
    const all = document.querySelectorAll('[role="tabpanel"]');
    expect(all).toHaveLength(3);
    expect([...all].map((p) => (p as HTMLElement).hidden)).toEqual([false, true, true]);
    expect(screen.getByText("Panel three", { selector: "p" })).toBeTruthy();
  });

  it("moves with the arrow keys, Home and End and wraps around", () => {
    const onSelect = setup();
    const press = (key: string, expected: string) => {
      fireEvent.keyDown(document.activeElement!, { key });
      expect(document.activeElement).toBe(tab(expected));
      expect(tab(expected).getAttribute("aria-selected")).toBe("true");
      expect(tab(expected).tabIndex).toBe(0);
    };
    tab("One").focus();
    press("ArrowRight", "Two");
    expect(onSelect).toHaveBeenLastCalledWith("two");
    expect(screen.getByRole("tabpanel", { name: "Two" })).toBeTruthy();
    press("ArrowRight", "Three");
    press("ArrowRight", "One");
    press("ArrowLeft", "Three");
    expect(onSelect).toHaveBeenLastCalledWith("three");
    press("Home", "One");
    press("End", "Three");
    expect(onSelect).toHaveBeenCalledTimes(6);
  });

  it("leaves other keys alone", () => {
    const onSelect = setup();
    for (const key of ["j", "Tab", "Enter"]) {
      const event = createEvent.keyDown(tab("One"), { key });
      fireEvent(tab("One"), event);
      expect(event.defaultPrevented).toBe(false);
    }
    expect(onSelect).not.toHaveBeenCalled();
    const arrow = createEvent.keyDown(tab("One"), { key: "ArrowRight" });
    fireEvent(tab("One"), arrow);
    expect(arrow.defaultPrevented).toBe(true);
  });

  it("selects with a click and keeps a panel's state", () => {
    const onSelect = setup();
    fireEvent.click(tab("Two"));
    expect(onSelect).toHaveBeenCalledWith("two");
    fireEvent.change(screen.getByLabelText("Field two"), { target: { value: "typed" } });
    fireEvent.click(tab("One"));
    expect(screen.getByRole("tabpanel", { name: "One" })).toBeTruthy();
    fireEvent.click(tab("Two"));
    expect((screen.getByLabelText("Field two") as HTMLInputElement).value).toBe("typed");
  });
});
