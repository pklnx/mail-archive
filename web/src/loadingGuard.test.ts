// @vitest-environment node
// No text-only loading state (#287): "Loading…" (t.loading) is never visible
// text in a view. A loading state is a skeleton with a visually hidden
// status (Loading in lazyView.tsx); t.loading may be only an accessible name
// (iconDescription, loadingIconDescription) or the text of a visually hidden
// element. Carbon shows description and loadingDescription as visible text,
// so they are not allowed.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = dirname(fileURLToPath(import.meta.url));

/** The uses of t.loading in code that the rule does not allow, as "line: text". */
export function visibleLoading(code: string): string[] {
  const bad: string[] = [];
  for (const m of code.matchAll(/\bt\.loading\b/g)) {
    const before = code.slice(0, m.index);
    const line = before.split("\n").length;
    // An accessible name: iconDescription={t.loading} or loadingIconDescription={t.loading}.
    if (/\b(?:loadingIconDescription|iconDescription)=\{\s*$/.test(before)) continue;
    // The only child of an element whose classes include cds--visually-hidden.
    const tag = /<([a-zA-Z]+)\b([^<>]*)>\s*\{\s*$/.exec(before);
    const after = code.slice(m.index! + m[0].length);
    if (tag && /\bclassName="(?:[^"]*\s)?cds--visually-hidden(?:\s[^"]*)?"/.test(tag[2]!) && new RegExp(`^\\s*\\}\\s*</${tag[1]}>`).test(after)) continue;
    bad.push(`${line}: ${code.split("\n")[line - 1]!.trim()}`);
  }
  return bad;
}

const sources = () =>
  readdirSync(src)
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
    .map((f) => ({ file: f, code: readFileSync(resolve(src, f), "utf8") }));

describe("loading states", () => {
  it("tells the allowed uses of t.loading from visible text", () => {
    const allowed = [
      `<InlineLoading description={t.checkingLogin} iconDescription={t.loading} />`,
      `<Modal loadingIconDescription={t.loading} />`,
      `<span className="cds--visually-hidden">{t.loading}</span>`,
      `<li className="cds--visually-hidden" role="status">\n  {t.loading}\n</li>`,
      `<p role="status" className="x cds--visually-hidden">{t.loading}</p>`,
    ];
    for (const code of allowed) expect(visibleLoading(code), code).toEqual([]);
    const visible = [
      `<p className="app-text-secondary">{t.loading}</p>`,
      `<InlineLoading description={t.loading} />`,
      `<Modal loadingDescription={t.loading} />`,
      `<Button>{busy ? t.loading : t.save}</Button>`,
      `const label = t.loading;`,
      `<span className="cds--visually-hidden-not">{t.loading}</span>`,
      `<span className="cds--visually-hidden">{t.loading} <b>x</b></span>`,
    ];
    for (const code of visible) expect(visibleLoading(code), code).toHaveLength(1);
  });

  it("has no visible Loading… in any view", () => {
    const files = sources();
    // Guards the scan: the views that announce loading are read.
    expect(files.filter((f) => /\bt\.loading\b/.test(f.code)).map((f) => f.file)).toEqual(expect.arrayContaining(["lazyView.tsx", "MessageList.tsx", "Auth.tsx"]));
    const bad = files.flatMap(({ file, code }) => visibleLoading(code).map((b) => `${file}:${b}`));
    expect(bad).toEqual([]);
  });
});
