// LazyView (#288): the skeleton while a lazy view loads, the load-error
// notice when its chunk does not, and every other error rethrown.
import { Component, act, type ReactNode } from "react";
import { SkeletonText } from "@carbon/react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChunkLoadError, LazyView, Loading, lazyNamed } from "./lazyView";
import { applySettings, messagesFor, t } from "./i18n";
import { expectLoaded, expectLoading } from "./test/loading";

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const View = ({ text }: { text: string }) => <h1>{text}</h1>;
type Mod = { View: typeof View };

/** Catches what LazyView rethrows, as the root would. */
class Outer extends Component<{ children: ReactNode }, { error: unknown }> {
  state: { error: unknown } = { error: null };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  render() {
    return this.state.error ? <p>outer: {String((this.state.error as Error).message)}</p> : this.props.children;
  }
}

beforeEach(() => {
  // React reports every caught render error on the console.
  vi.spyOn(console, "error").mockImplementation(() => {});
  vi.spyOn(console, "warn").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  applySettings({ language: "en", locale: "en" });
});

describe("LazyView", () => {
  it("shows a skeleton with a hidden status while the view loads, then the view", async () => {
    const load = deferred<Mod>();
    const Lazy = lazyNamed(() => load.promise, "View");
    const { container } = render(
      <LazyView>
        <Lazy text="Loaded" />
      </LazyView>,
    );
    const fallback = expectLoading(container)!;
    expect(fallback.className).toContain("page");
    expect(fallback.querySelectorAll(".cds--skeleton__text").length).toBeGreaterThan(1);
    expect(fallback.querySelector(".cds--skeleton__heading")).not.toBeNull();
    // Screen readers hear "Loading…"; on screen there are only skeletons.
    expect(screen.getByRole("status").textContent).toBe(t.loading);
    expect(container.querySelector("[aria-busy]")).toBeNull();
    await act(async () => load.resolve({ View }));
    expect(await screen.findByRole("heading", { name: "Loaded" })).toBeTruthy();
    expectLoaded(container);
  });

  it("shows the load-error notice when the chunk does not load; Retry reloads; another view still loads", async () => {
    const reload = vi.fn();
    const Broken = lazyNamed<Mod, "View">(() => Promise.reject(new TypeError("Failed to fetch dynamically imported module")), "View");
    const { rerender } = render(
      <LazyView key="a" reload={reload}>
        <Broken text="never" />
      </LazyView>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(t.viewLoadFailed);
    expect(alert.textContent).toContain(t.viewLoadFailedText);
    // A notice in the page, not closable: Retry is the way on.
    expect(screen.queryByRole("button", { name: t.carbon.closeNotification })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t.retry }));
    expect(reload).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("heading", { name: "never" })).toBeNull();

    const Fine = lazyNamed(() => Promise.resolve({ View }), "View");
    rerender(
      <LazyView key="b" reload={reload}>
        <Fine text="Other view" />
      </LazyView>,
    );
    expect(await screen.findByRole("heading", { name: "Other view" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("rethrows an error that is not a failed import: a bug in a view is not 'could not be loaded'", async () => {
    const Buggy = lazyNamed(
      () =>
        Promise.resolve({
          View: () => {
            throw new Error("bug in the view");
          },
        }),
      "View",
    );
    render(
      <Outer>
        <LazyView reload={() => {}}>
          <Buggy />
        </LazyView>
      </Outer>,
    );
    expect(await screen.findByText("outer: bug in the view")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByText(t.viewLoadFailed)).toBeNull();
  });

  it("wraps only the import's rejection as a ChunkLoadError", async () => {
    const cause = new TypeError("404");
    const Broken = lazyNamed<Mod, "View">(() => Promise.reject(cause), "View");
    render(
      <Outer>
        <Broken text="x" />
      </Outer>,
    );
    // Outside a LazyView the error reaches the root as it is, wrapped.
    expect(await screen.findByText(/outer: chunk did not load: 404/)).toBeTruthy();
    const err = new ChunkLoadError(cause);
    expect(err.cause).toBe(cause);
  });

  it("floats the notice for an overlay, with a close button", async () => {
    const onClose = vi.fn();
    const Broken = lazyNamed<Mod, "View">(() => Promise.reject(new TypeError("404")), "View");
    render(
      <LazyView fallback={null} onClose={onClose}>
        <Broken text="x" />
      </LazyView>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert.closest(".app-load-error--floating")?.parentElement).toBe(document.body);
    fireEvent.click(screen.getByRole("button", { name: t.carbon.closeNotification }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("speaks German", async () => {
    applySettings({ language: "de", locale: "de" });
    const de = messagesFor("de");
    const Broken = lazyNamed<Mod, "View">(() => Promise.reject(new TypeError("404")), "View");
    render(
      <LazyView reload={() => {}}>
        <Broken text="x" />
      </LazyView>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Diese Seite konnte nicht geladen werden");
    expect(alert.textContent).toContain("Die App wurde vielleicht aktualisiert. Zum Fortfahren neu laden.");
    expect(screen.getByRole("button", { name: de.retry }).textContent).toBe("Erneut versuchen");
  });
});

describe("Loading", () => {
  it("is a status with the hidden text and the skeleton hidden from screen readers", () => {
    const { container } = render(
      <Loading>
        <SkeletonText paragraph lineCount={2} />
      </Loading>,
    );
    const status = expectLoading(container)!;
    expect(status.querySelector(".cds--visually-hidden")!.textContent).toBe(t.loading);
    expect(status.querySelector(".cds--skeleton__text")!.closest("[aria-hidden]")).not.toBeNull();
  });

  it("with announce={false} is only the skeleton, without a status", () => {
    const { container } = render(
      <Loading announce={false}>
        <SkeletonText paragraph lineCount={2} />
      </Loading>,
    );
    expectLoading(container, { announce: false });
    expect(screen.queryByRole("status")).toBeNull();
    expect(container.textContent).toBe("");
  });

  it("says Loading in German", () => {
    applySettings({ language: "de", locale: "de" });
    render(
      <Loading>
        <SkeletonText />
      </Loading>,
    );
    expect(screen.getByRole("status").textContent).toBe(messagesFor("de").loading);
  });
});
