// Line icons on a 24 grid (stroke 1.75), drawn in the current text color.

const props = {
  width: 18,
  height: 18,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.75,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
  "aria-hidden": true,
};

export const MenuIcon = () => (
  <svg {...props}>
    <path d="M4 6h16M4 12h16M4 18h16" />
  </svg>
);

export const BackIcon = () => (
  <svg {...props}>
    <path d="M15 18l-6-6 6-6" />
  </svg>
);

export const UpIcon = () => (
  <svg {...props}>
    <path d="M18 15l-6-6-6 6" />
  </svg>
);

export const DownIcon = () => (
  <svg {...props}>
    <path d="M6 9l6 6 6-6" />
  </svg>
);

export const SearchIcon = () => (
  <svg {...props}>
    <circle cx="11" cy="11" r="7" />
    <path d="M20 20l-3.5-3.5" />
  </svg>
);

/** A square icon button, 36px with a mouse and 44px on touch screens. */
export const iconButton =
  "flex size-9 shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-surface-hover hover:text-ink disabled:opacity-40 disabled:hover:bg-transparent pointer-coarse:size-11";
