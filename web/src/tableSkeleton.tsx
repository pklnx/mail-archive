import type { ReactNode } from "react";
import { DataTableSkeleton } from "@carbon/react";
import { md, useMediaQuery } from "./breakpoints";
import { Loading } from "./lazyView";

// The skeleton of a table while its rows load (#287). Only the lazy views
// import this module, so Carbon's DataTableSkeleton stays out of the
// initial bundle.

export interface SkeletonColumn {
  /** The column's header, as the real table has it. */
  header: ReactNode;
  /** The column is .app-wide-only in the real table: hidden below md. */
  wide?: boolean;
}

/**
 * Carbon's DataTableSkeleton with the real table's headers and size, in
 * Loading's status. Below md it has only the columns the table shows there.
 * No header or toolbar: Carbon's toolbar brings an English aria-label.
 */
export function TableSkeleton({ columns, size = "lg", announce }: { columns: SkeletonColumn[]; size?: "sm" | "md" | "lg"; announce?: boolean }) {
  const wide = useMediaQuery(md);
  const shown = wide ? columns : columns.filter((c) => !c.wide);
  return (
    <Loading announce={announce}>
      <DataTableSkeleton
        headers={shown.map((c) => ({ header: c.header }))}
        columnCount={shown.length}
        size={size}
        showHeader={false}
        showToolbar={false}
      />
    </Loading>
  );
}
