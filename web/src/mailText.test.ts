import { describe, expect, it } from "vitest";
import { attachmentKind, parseMailText, splitLinks } from "./mailText";

describe("parseMailText", () => {
  it("separates text, quotes and the signature", () => {
    const mail = "Hi Tim,\n\nsounds good.\n\nOn Oct 8 Tim wrote:\n> Saturday?\n> Or Sunday?\n\nBest\nAnna\n-- \nAnna Berger\nExample GmbH\n";
    expect(parseMailText(mail)).toEqual([
      { kind: "text", text: "Hi Tim,\n\nsounds good.\n\nOn Oct 8 Tim wrote:" },
      { kind: "quote", blocks: [{ kind: "text", text: "Saturday?\nOr Sunday?" }] },
      { kind: "text", text: "Best\nAnna" },
      { kind: "signature", text: "Anna Berger\nExample GmbH" },
    ]);
  });
  it("nests deeper quotes", () => {
    expect(parseMailText("> Yes\n> > Shall we?\n>> Really?\n> OK")).toEqual([
      {
        kind: "quote",
        blocks: [
          { kind: "text", text: "Yes" },
          { kind: "quote", blocks: [{ kind: "text", text: "Shall we?\nReally?" }] },
          { kind: "text", text: "OK" },
        ],
      },
    ]);
  });
  it("keeps plain text and CRLF line ends", () => {
    expect(parseMailText("a\r\nb")).toEqual([{ kind: "text", text: "a\nb" }]);
    expect(parseMailText("")).toEqual([]);
  });
  it("only takes the separator line as signature start", () => {
    expect(parseMailText("a -- b\n--x")).toEqual([{ kind: "text", text: "a -- b\n--x" }]);
  });
});

describe("splitLinks", () => {
  it("finds http(s) links without trailing punctuation", () => {
    expect(splitLinks("See https://example.org/a?b=1. Or (http://x.test)")).toEqual([
      { text: "See " },
      { text: "https://example.org/a?b=1", href: "https://example.org/a?b=1" },
      { text: ". Or (" },
      { text: "http://x.test", href: "http://x.test" },
      { text: ")" },
    ]);
  });
  it("leaves other schemes as text", () => {
    expect(splitLinks("javascript:alert(1) mailto:a@b")).toEqual([{ text: "javascript:alert(1) mailto:a@b" }]);
  });
});

describe("attachmentKind", () => {
  it("labels by extension, then by type", () => {
    expect(attachmentKind("Vertrag.pdf", "application/pdf")).toEqual({ label: "PDF", image: false });
    expect(attachmentKind("photo.JPEG", "image/jpeg")).toEqual({ label: "JPEG", image: true });
    expect(attachmentKind(undefined, "application/zip")).toEqual({ label: "ZIP", image: false });
    expect(attachmentKind("", "image/png")).toEqual({ label: "PNG", image: true });
    expect(attachmentKind("noext", "application/octet-stream")).toEqual({ label: "", image: false });
  });
});
