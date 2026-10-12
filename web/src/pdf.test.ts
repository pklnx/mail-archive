import { describe, expect, it } from "vitest";
import { onePagePdf, pdfString } from "./pdf";

const ascii = (b: Uint8Array, from = 0, to = b.length) => String.fromCharCode(...b.subarray(from, to));

/** Byte offset of the first occurrence of s at or after from. */
function indexOf(b: Uint8Array, s: string, from = 0): number {
  outer: for (let i = from; i <= b.length - s.length; i++) {
    for (let j = 0; j < s.length; j++) if (b[i + j] !== s.charCodeAt(j)) continue outer;
    return i;
  }
  return -1;
}

describe("onePagePdf", () => {
  const pdf = onePagePdf([
    { font: "sansBold", size: 18, x: 56, y: 785, text: "Grüße – (Test)" },
    { font: "mono", size: 16, x: 56, y: 680, text: " 1   abcd-efgh-jkmn-pqrs" },
  ]);

  it("has the header, a binary line and the end marker", () => {
    expect(ascii(pdf, 0, 9)).toBe("%PDF-1.4\n");
    expect(Array.from(pdf.subarray(10, 14)).every((b) => b > 0x7f)).toBe(true);
    expect(ascii(pdf, pdf.length - 6)).toBe("%%EOF\n");
  });

  it("has a byte-exact cross-reference table", () => {
    const xref = indexOf(pdf, "xref\n0 8\n");
    expect(xref).toBeGreaterThan(0);
    const startxref = indexOf(pdf, "startxref\n");
    expect(Number(ascii(pdf, startxref + 10, indexOf(pdf, "\n", startxref + 10)))).toBe(xref);
    const table = xref + "xref\n0 8\n".length;
    expect(ascii(pdf, table, table + 20)).toBe("0000000000 65535 f \n");
    for (let n = 1; n <= 7; n++) {
      const entry = ascii(pdf, table + 20 * n, table + 20 * (n + 1));
      expect(entry).toMatch(/^\d{10} 00000 n \n$/);
      const off = Number(entry.slice(0, 10));
      expect(ascii(pdf, off, off + `${n} 0 obj\n`.length)).toBe(`${n} 0 obj\n`);
    }
    expect(ascii(pdf)).toContain("trailer\n<< /Size 8 /Root 1 0 R >>");
  });

  it("gives the stream's exact length", () => {
    const start = indexOf(pdf, "stream\n") + "stream\n".length;
    const end = indexOf(pdf, "\nendstream");
    const length = Number(/\/Length (\d+)/.exec(ascii(pdf))![1]);
    expect(end - start).toBe(length);
    expect(ascii(pdf, start, end)).toContain("/F2 18 Tf 56 785 Td (Gr\\374\\337e \\226 \\(Test\\)) Tj");
    expect(ascii(pdf, start, end)).toContain("/F3 16 Tf 56 680 Td");
  });
});

describe("pdfString", () => {
  it("escapes and encodes in WinAnsi", () => {
    expect(pdfString("a(b)c\\d")).toBe("(a\\(b\\)c\\\\d)");
    expect(pdfString("ä")).toBe("(\\344)");
    expect(pdfString("–")).toBe("(\\226)");
    expect(pdfString("„“")).toBe("(\\204\\223)");
    expect(pdfString("€")).toBe("(\\200)");
    expect(pdfString("😀")).toBe("(?)");
    expect(pdfString("5:41 PM x")).toBe("(5:41 PM x)");
    expect(pdfString("\ttab")).toBe("(?tab)");
    expect(pdfString("Grüße – „x“ € 😀  ")).toMatch(/^[\x20-\x7e]*$/);
  });
});
