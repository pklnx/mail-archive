// The recovery code sheet, as PDF and plain text. Built in the browser from
// the codes on screen: the server keeps only their hashes and never sends
// them again.
import { locale as uiLocale, t, type Messages } from "./i18n";
import { onePagePdf, pageWidth, type PdfText } from "./pdf";

export interface RecoverySheet {
  user: string;
  origin: string;
  createdAt: Date;
  codes: string[];
}

const margin = 56;
const valueX = 140;
// Courier is 0.6 × size wide: 60 characters of 11 pt fit between valueX and the right margin.
const valueChars = Math.floor((pageWidth - margin - valueX) / (0.6 * 11));

function formatCreated(d: Date, loc: string): string {
  return new Intl.DateTimeFormat(loc, { dateStyle: "long", timeStyle: "short" }).format(d);
}

function title(m: Messages): string {
  return `${m.appName} – ${m.twoFactorRecovery}`;
}

function fields(sheet: RecoverySheet, m: Messages, loc: string): [string, string][] {
  return [
    [m.recoverySheetUser, sheet.user],
    [m.recoverySheetServer, sheet.origin],
    [m.recoverySheetCreated, formatCreated(sheet.createdAt, loc)],
  ];
}

function codeLine(code: string, i: number): string {
  return `${String(i + 1).padStart(2)}   ${code}`;
}

/** Splits s into pieces of at most n characters. */
export function wrap(s: string, n: number): string[] {
  const out: string[] = [];
  for (let i = 0; i < s.length; i += n) out.push(s.slice(i, i + n));
  return out.length ? out : [""];
}

/** The sheet as a one-page A4 PDF, in the language of m and the date format of loc. */
export function recoveryPdf(sheet: RecoverySheet, m: Messages = t, loc: string = uiLocale): Uint8Array<ArrayBuffer> {
  const lines: PdfText[] = [{ font: "sansBold", size: 18, x: margin, y: 785, text: title(m) }];
  let y = 750;
  for (const [label, value] of fields(sheet, m, loc)) {
    lines.push({ font: "sansBold", size: 11, x: margin, y, text: label });
    for (const part of wrap(value, valueChars)) {
      lines.push({ font: "mono", size: 11, x: valueX, y, text: part });
      y -= 16;
    }
  }
  y -= 22;
  sheet.codes.forEach((code, i) => {
    lines.push({ font: "mono", size: 16, x: margin, y, text: codeLine(code, i) });
    y -= 26;
  });
  y -= 20;
  for (const sentence of m.recoverySheetNote) {
    lines.push({ font: "sans", size: 10, x: margin, y, text: sentence });
    y -= 15;
  }
  const footer = `${m.appName} · ${sheet.origin}`;
  lines.push({ font: "sans", size: 8, x: margin, y: 40, text: footer.length > 100 ? footer.slice(0, 99) + "…" : footer });
  return onePagePdf(lines);
}

/** The same sheet as plain text. */
export function recoveryText(sheet: RecoverySheet, m: Messages = t, loc: string = uiLocale): string {
  const f = fields(sheet, m, loc);
  const width = Math.max(...f.map(([label]) => label.length)) + 2;
  return [
    title(m),
    "",
    ...f.map(([label, value]) => `${(label + ":").padEnd(width)}${value}`),
    "",
    ...sheet.codes.map(codeLine),
    "",
    ...m.recoverySheetNote,
    "",
  ].join("\n");
}

/** mail-archive-recovery-codes-<user>-<YYYY-MM-DD>.<ext>, with the local date. */
export function recoveryFileName(user: string, createdAt: Date, ext: "pdf" | "txt"): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  const date = `${createdAt.getFullYear()}-${pad(createdAt.getMonth() + 1)}-${pad(createdAt.getDate())}`;
  return `mail-archive-recovery-codes-${user}-${date}.${ext}`;
}

/** Offers data as a file download, without a request. */
export function saveFile(data: BlobPart, name: string, type: string): void {
  const url = URL.createObjectURL(new Blob([data], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.style.display = "none";
  document.body.append(a); // a detached anchor does not download in older Firefox
  a.click();
  a.remove();
  // Revoking at once breaks the download in some browsers.
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
