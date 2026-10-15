// Structure of a plain-text mail for reading: quoted replies (">" lines) and
// the signature (after the "-- " line) are set apart from the text itself.

export type TextBlock =
  | { kind: "text"; text: string }
  | { kind: "quote"; blocks: TextBlock[] }
  | { kind: "signature"; text: string };

/** Quote depth of a line: the number of leading ">" markers, spaces between them allowed. */
function depth(line: string): number {
  const m = /^(?:[ \t]*>)+/.exec(line);
  return m ? (m[0].match(/>/g) ?? []).length : 0;
}

/** Removes one quote level and the space after it. */
function unquote(line: string): string {
  return line.replace(/^[ \t]*> ?/, "");
}

function blocks(lines: string[]): TextBlock[] {
  const out: TextBlock[] = [];
  const quoted = lines.map((l) => depth(l) > 0);
  let i = 0;
  while (i < lines.length) {
    const start = i;
    const quote = quoted[i];
    while (i < lines.length && quoted[i] === quote) i++;
    if (quote) {
      out.push({ kind: "quote", blocks: blocks(lines.slice(start, i).map(unquote)) });
      continue;
    }
    const text = trimBlankLines(lines.slice(start, i)).join("\n");
    if (text) out.push({ kind: "text", text });
  }
  return out;
}

function trimBlankLines(lines: string[]): string[] {
  let a = 0;
  let b = lines.length;
  while (a < b && !lines[a]?.trim()) a++;
  while (b > a && !lines[b - 1]?.trim()) b--;
  return lines.slice(a, b);
}

/** Splits a plain-text body into text, quotes (nested) and the signature. */
export function parseMailText(text: string): TextBlock[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  // The usual signature separator; "--" without the space is common too.
  const sig = lines.findIndex((l) => l === "-- " || l === "--");
  const body = sig < 0 ? lines : lines.slice(0, sig);
  const out = blocks(body);
  if (sig >= 0) {
    const signature = trimBlankLines(lines.slice(sig + 1)).join("\n");
    if (signature) out.push({ kind: "signature", text: signature });
  }
  return out;
}

/** Splits text into plain parts and http(s) links, which the view makes clickable. */
export function splitLinks(text: string): { text: string; href?: string }[] {
  const out: { text: string; href?: string }[] = [];
  let last = 0;
  for (const m of text.matchAll(/https?:\/\/[^\s<>"]+/g)) {
    // Closing punctuation belongs to the sentence, not the link.
    const url = m[0].replace(/[.,;:!?)\]}'»]+$/, "");
    if (m.index > last) out.push({ text: text.slice(last, m.index) });
    out.push({ text: url, href: url });
    last = m.index + url.length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

const typeLabels: Record<string, string> = {
  "application/pdf": "PDF",
  "application/zip": "ZIP",
  "text/calendar": "ICS",
  "text/plain": "TXT",
  "text/csv": "CSV",
  "message/rfc822": "EML",
};

/** A short type label for an attachment tile ("PDF", "DOCX"), and whether it is an image. */
export function attachmentKind(filename: string | undefined, contentType: string): { label: string; image: boolean } {
  const image = contentType.startsWith("image/");
  const ext = /\.([a-z0-9]{1,5})$/i.exec(filename ?? "")?.[1];
  const label = ext ? ext.toUpperCase() : (typeLabels[contentType] ?? (image ? contentType.slice(6).toUpperCase() : ""));
  return { label: label.slice(0, 5), image };
}
