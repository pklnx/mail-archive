// A minimal PDF writer: one A4 page of text in the 14 standard fonts, which
// every PDF reader has, so nothing is embedded. Enough for a printable sheet,
// without a PDF library.
//
// Byte rules: every character of the file is below 0x100 and becomes one
// byte (never through TextEncoder or a Blob of a string), offsets are counted
// on the bytes, and every xref entry is exactly 20 bytes.

export type PdfFont = "sans" | "sansBold" | "mono";

export interface PdfText {
  font: PdfFont;
  size: number;
  x: number;
  y: number;
  text: string;
}

/** A4 in points. */
export const pageWidth = 595.28;
export const pageHeight = 841.89;

const fontNames: Record<PdfFont, string> = { sans: "F1", sansBold: "F2", mono: "F3" };

// WinAnsiEncoding (PDF 32000-1, Annex D) where it differs from Latin-1.
const winAnsi = new Map<number, number>([
  [0x20ac, 0x80], [0x201a, 0x82], [0x0192, 0x83], [0x201e, 0x84], [0x2026, 0x85], [0x2020, 0x86],
  [0x2021, 0x87], [0x02c6, 0x88], [0x2030, 0x89], [0x0160, 0x8a], [0x2039, 0x8b], [0x0152, 0x8c],
  [0x017d, 0x8e], [0x2018, 0x91], [0x2019, 0x92], [0x201c, 0x93], [0x201d, 0x94], [0x2022, 0x95],
  [0x2013, 0x96], [0x2014, 0x97], [0x02dc, 0x98], [0x2122, 0x99], [0x0161, 0x9a], [0x203a, 0x9b],
  [0x0153, 0x9c], [0x017e, 0x9e], [0x0178, 0x9f],
  // No-break spaces, as Intl puts them into dates ("5:41 PM").
  [0x00a0, 0x20], [0x202f, 0x20], [0x2007, 0x20],
]);

/**
 * Text as a PDF string literal in WinAnsiEncoding: ( ) \ escaped, bytes
 * outside ASCII as octal, characters WinAnsi lacks as "?". The result is ASCII.
 */
export function pdfString(s: string): string {
  let out = "(";
  for (const ch of s) {
    const b = winAnsiByte(ch.codePointAt(0)!);
    if (b === 0x28 || b === 0x29 || b === 0x5c) out += "\\" + String.fromCharCode(b);
    else if (b < 0x80) out += String.fromCharCode(b);
    else out += "\\" + b.toString(8);
  }
  return out + ")";
}

function winAnsiByte(cp: number): number {
  const mapped = winAnsi.get(cp);
  if (mapped !== undefined) return mapped;
  if ((cp >= 0x20 && cp < 0x7f) || (cp >= 0xa1 && cp <= 0xff)) return cp;
  return 0x3f; // "?"
}

function num(n: number): string {
  return String(Math.round(n * 100) / 100);
}

/** A one-page A4 PDF with the standard fonts, text only. */
export function onePagePdf(lines: PdfText[]): Uint8Array<ArrayBuffer> {
  const content = lines
    .map((l) => `BT /${fontNames[l.font]} ${num(l.size)} Tf ${num(l.x)} ${num(l.y)} Td ${pdfString(l.text)} Tj ET`)
    .join("\n");
  const font = (base: string) => `<< /Type /Font /Subtype /Type1 /BaseFont /${base} /Encoding /WinAnsiEncoding >>`;
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 ${pageWidth} ${pageHeight}] ` +
      "/Resources << /Font << /F1 4 0 R /F2 5 0 R /F3 6 0 R >> >> /Contents 7 0 R >>",
    font("Helvetica"),
    font("Helvetica-Bold"),
    font("Courier"),
    `<< /Length ${content.length} >>\nstream\n${content}\nendstream`,
  ];

  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let size = 0;
  const write = (s: string) => {
    const b = latin1(s);
    chunks.push(b);
    size += b.length;
  };
  // The second line has bytes above 0x7F, so tools treat the file as binary.
  write("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n");
  const offsets: number[] = [];
  objects.forEach((body, i) => {
    offsets.push(size);
    write(`${i + 1} 0 obj\n${body}\nendobj\n`);
  });
  const xref = size;
  write(`xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`);
  for (const off of offsets) write(`${String(off).padStart(10, "0")} 00000 n \n`);
  write(`trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`);

  const out = new Uint8Array(size);
  let at = 0;
  for (const c of chunks) {
    out.set(c, at);
    at += c.length;
  }
  return out;
}

function latin1(s: string): Uint8Array<ArrayBuffer> {
  const b = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c > 0xff) throw new Error("pdf: character above 0xFF");
    b[i] = c;
  }
  return b;
}
