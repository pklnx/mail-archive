import { useId, useRef, type KeyboardEvent, type ReactNode } from "react";

export interface Tab<Id extends string> {
  id: Id;
  label: string;
  panel: ReactNode;
}

const tab = "shrink-0 whitespace-nowrap rounded-md px-3 py-1 text-sm pointer-coarse:min-h-11";

/**
 * Tabs after the WAI-ARIA tab pattern with automatic activation: the arrow
 * keys, Home and End move the selection and focus. All panels stay mounted
 * and the inactive ones are hidden, so their state survives a switch.
 */
export function Tabs<Id extends string>({
  label,
  tabs,
  selected,
  onSelect,
}: {
  label: string;
  tabs: Tab<Id>[];
  selected: Id;
  onSelect: (id: Id) => void;
}) {
  const base = useId();
  const buttons = useRef(new Map<Id, HTMLButtonElement>());
  const tabId = (id: Id) => `${base}-tab-${id}`;
  const panelId = (id: Id) => `${base}-panel-${id}`;

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = tabs.findIndex((x) => x.id === selected);
    let next: number;
    switch (e.key) {
      case "ArrowRight":
        next = (i + 1) % tabs.length;
        break;
      case "ArrowLeft":
        next = (i - 1 + tabs.length) % tabs.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = tabs.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    const id = tabs[next]!.id;
    onSelect(id);
    buttons.current.get(id)?.focus();
  };

  return (
    <>
      {/* Vertical padding keeps the focus ring inside the scrolling list. */}
      <div role="tablist" aria-label={label} className="-mx-1 flex gap-1 overflow-x-auto p-1" onKeyDown={onKeyDown}>
        {tabs.map((x) => (
          <button
            key={x.id}
            ref={(el) => {
              if (el) buttons.current.set(x.id, el);
              else buttons.current.delete(x.id);
            }}
            type="button"
            role="tab"
            id={tabId(x.id)}
            aria-selected={x.id === selected}
            aria-controls={panelId(x.id)}
            tabIndex={x.id === selected ? 0 : -1}
            className={`${tab} ${x.id === selected ? "bg-line font-medium" : "hover:bg-surface-hover"}`}
            onClick={() => onSelect(x.id)}
          >
            {x.label}
          </button>
        ))}
      </div>
      {tabs.map((x) => (
        <div
          key={x.id}
          role="tabpanel"
          id={panelId(x.id)}
          aria-labelledby={tabId(x.id)}
          tabIndex={0}
          hidden={x.id !== selected}
          className="flex flex-col gap-4"
        >
          {x.panel}
        </div>
      ))}
    </>
  );
}
