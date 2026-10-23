import { splitHighlights } from "./format";

export function Highlight({ text }: { text: string }) {
  return (
    <>
      {splitHighlights(text).map((s, i) =>
        s.match ? (
          <mark key={i} className="app-highlight">
            {s.text}
          </mark>
        ) : (
          <span key={i}>{s.text}</span>
        ),
      )}
    </>
  );
}
