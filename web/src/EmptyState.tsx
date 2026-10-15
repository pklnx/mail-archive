import type { ReactNode } from "react";

interface Props {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  /** Buttons for the next step. */
  actions?: ReactNode;
}

/** Says why there is nothing to show and offers the next step. */
export function EmptyState({ icon, title, children, actions }: Props) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
      <span className="mb-1 grid size-12 place-items-center rounded-full bg-surface-hover text-ink-muted">{icon}</span>
      <p className="font-semibold">{title}</p>
      {children && <div className="max-w-xs text-sm text-ink-muted">{children}</div>}
      {actions && <div className="mt-2 flex flex-wrap justify-center gap-2">{actions}</div>}
    </div>
  );
}
