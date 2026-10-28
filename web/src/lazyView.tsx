// Views that load on demand (#288): their code is a chunk of its own, which
// the browser fetches the first time the view shows.
import { Component, Suspense, lazy, type ComponentType, type ErrorInfo, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Button, InlineNotification, SkeletonText } from "@carbon/react";
import { t } from "./i18n";

/**
 * A lazy chunk that did not load: the rejection of its import(), for
 * example a 404 for a chunk name that a server upgrade replaced. Only this
 * error shows the load-error notice; any other error is rethrown.
 */
export class ChunkLoadError extends Error {
  constructor(cause: unknown) {
    super(`chunk did not load: ${cause instanceof Error ? cause.message : String(cause)}`, { cause });
    this.name = "ChunkLoadError";
  }
}

/**
 * React.lazy for a named export. Only the import's rejection becomes a
 * ChunkLoadError; an error inside the view while it renders stays what it is.
 */
export function lazyNamed<M extends Record<K, ComponentType<any>>, K extends keyof M & string>(load: () => Promise<M>, name: K) {
  return lazy(() =>
    load().then(
      (m) => ({ default: m[name] }),
      (err: unknown) => {
        throw new ChunkLoadError(err);
      },
    ),
  );
}

/** While a view loads: Carbon skeletons in the view's page frame (none for a form in a column), no text. */
export function ViewFallback({ inline = false }: { inline?: boolean }) {
  return (
    <div className={inline ? "app-view-fallback" : "page app-view-fallback"} aria-busy="true">
      <SkeletonText heading width="40%" />
      <SkeletonText paragraph lineCount={3} />
    </div>
  );
}

interface BoundaryProps {
  children: ReactNode;
  /** Reloads the page; injectable for tests. */
  reload?: () => void;
  /** Set for an overlay (the shortcuts): the notice floats and can be closed. */
  onClose?: () => void;
  /** A form in a column (the login screens), not a page: no page frame. */
  inline?: boolean;
}

/** Shows the load-error notice for a ChunkLoadError and rethrows every other error. */
export class LazyBoundary extends Component<BoundaryProps, { error: unknown }> {
  state: { error: unknown } = { error: null };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    if (error instanceof ChunkLoadError) console.warn(error.message, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (error === null) return this.props.children;
    // Not a failed import: a bug in the view. It goes on to the root as before.
    if (!(error instanceof ChunkLoadError)) throw error;
    const { reload = () => location.reload(), onClose, inline } = this.props;
    // Carbon's InlineNotification takes no interactive children: Retry sits under it.
    const notice = (
      <>
        <InlineNotification
          kind="error"
          lowContrast
          role="alert"
          title={t.viewLoadFailed}
          subtitle={t.viewLoadFailedText}
          hideCloseButton={!onClose}
          aria-label={onClose ? t.carbon.closeNotification : undefined}
          onClose={
            onClose
              ? () => {
                  onClose();
                  return false;
                }
              : undefined
          }
          statusIconDescription={t.carbon.status.error}
        />
        <div className="app-load-error__action">
          <Button kind="tertiary" size="sm" onClick={reload}>
            {t.retry}
          </Button>
        </div>
      </>
    );
    if (onClose) return createPortal(<div className="app-load-error app-load-error--floating">{notice}</div>, document.body);
    return <div className={inline ? "app-load-error" : "page app-load-error"}>{notice}</div>;
  }
}

/** A lazy view in its Suspense, with the skeleton while it loads and the notice if it cannot. */
export function LazyView({ children, fallback, reload, onClose, inline }: BoundaryProps & { fallback?: ReactNode }) {
  return (
    <LazyBoundary reload={reload} onClose={onClose} inline={inline}>
      <Suspense fallback={fallback === undefined ? <ViewFallback inline={inline} /> : fallback}>{children}</Suspense>
    </LazyBoundary>
  );
}
