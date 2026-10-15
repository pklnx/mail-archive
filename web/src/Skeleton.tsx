// Placeholders in the shape of the content that is loading, so the layout
// does not jump when it arrives. Screen readers hear the label instead.
import { t } from "./i18n";

function Bar({ w, h = "h-3", round = false }: { w: string; h?: string; round?: boolean }) {
  return <span className={`block animate-pulse bg-surface-hover motion-reduce:animate-none ${round ? "rounded-full" : "rounded"} ${w} ${h}`} />;
}

/** Rows of the message list. */
export function MessageRowsSkeleton({ rows }: { rows: number }) {
  return Array.from({ length: rows }, (_, i) => (
    <li key={i} aria-hidden className="flex flex-col gap-2 border-b border-line px-4 py-3.5">
      <span className="flex justify-between gap-4">
        <Bar w={["w-32", "w-24", "w-40"][i % 3] ?? "w-32"} />
        <Bar w="w-12" />
      </span>
      <Bar w={["w-3/4", "w-2/3", "w-1/2"][i % 3] ?? "w-3/4"} />
      <Bar w="w-5/6" h="h-2.5" />
    </li>
  ));
}

/** The header and first lines of a message. */
export function MessageSkeleton() {
  return (
    <div role="status" className="flex flex-col gap-3 p-4">
      <span className="sr-only">{t.loading}</span>
      <Bar w="w-2/3" h="h-5" />
      <Bar w="w-1/2" />
      <Bar w="w-2/5" />
      <Bar w="w-1/3" />
      <div className="mt-6 flex max-w-2xl flex-col gap-2.5">
        <Bar w="w-full" />
        <Bar w="w-11/12" />
        <Bar w="w-4/5" />
      </div>
    </div>
  );
}

/** Account and folder rows in the sidebar. */
export function SidebarSkeleton() {
  return (
    <div role="status" className="flex flex-col gap-3 px-2 py-1">
      <span className="sr-only">{t.loading}</span>
      {["w-28", "w-16", "w-20", "w-32", "w-16"].map((w, i) => (
        <Bar key={i} w={w} />
      ))}
    </div>
  );
}
