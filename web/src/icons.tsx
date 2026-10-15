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

export const CloseIcon = () => (
  <svg {...props}>
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);

export const FilterIcon = () => (
  <svg {...props}>
    <path d="M3 5h18l-7 8.5V19l-4 2v-7.5z" />
  </svg>
);

export const InboxIcon = () => (
  <svg {...props}>
    <path d="M22 12h-6l-2 3h-4l-2-3H2" />
    <path d="M5.5 5.1L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.7 4H7.3a2 2 0 0 0-1.8 1.1z" />
  </svg>
);

export const FolderIcon = () => (
  <svg {...props}>
    <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
  </svg>
);

export const ServerIcon = () => (
  <svg {...props}>
    <rect x="3" y="4" width="18" height="7" rx="2" />
    <rect x="3" y="13" width="18" height="7" rx="2" />
    <path d="M7 7.5h.01M7 16.5h.01" />
  </svg>
);

export const MoreIcon = () => (
  <svg {...props}>
    <circle cx="5" cy="12" r="1" />
    <circle cx="12" cy="12" r="1" />
    <circle cx="19" cy="12" r="1" />
  </svg>
);

/** A square icon button, 36px with a mouse and 44px on touch screens. */
export const iconButton =
  "flex size-9 shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-surface-hover hover:text-ink disabled:opacity-40 disabled:hover:bg-transparent pointer-coarse:size-11";
