// Suggests a short account name from the login or the server, so that
// commands like `./ma sync --account NAME` stay easy to type.

// Second-level labels that are not a name (example.co.uk → example).
const secondLevel = new Set(["co", "com", "org", "net", "ac", "gov", "edu"]);
// Host labels that say nothing about the provider.
const genericHost = new Set(["imap", "imaps", "mail", "mx", "me", "email", "secure"]);

function clean(label: string): string {
  return label
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
}

/** The label that names the domain: the one before the public suffix. */
function domainLabel(labels: string[]): string {
  if (labels.length < 2) return "";
  let i = labels.length - 2;
  if (i > 0 && secondLevel.has(labels[i] ?? "")) i--;
  return labels[i] ?? "";
}

export function suggestName(username: string, host: string): string {
  const at = username.lastIndexOf("@");
  if (at >= 0) {
    const name = clean(domainLabel(username.slice(at + 1).split(".")));
    if (name) return name;
  }
  const h = host.trim().toLowerCase();
  if (!h || /^[\d.]+$/.test(h) || h.includes(":")) return ""; // IP address
  const labels = h.split(".");
  const tld = labels.pop() ?? "";
  const rest = labels.filter((l) => !genericHost.has(l));
  return rest.length ? clean(domainLabel([...rest, tld])) : "";
}

const separators = /[\s._\-@]+/u;

/** Splits into user-perceived characters, so emoji and flags stay whole; code points without Intl.Segmenter. */
export function graphemes(s: string, segmenter = typeof Intl !== "undefined" && "Segmenter" in Intl): string[] {
  if (segmenter) return Array.from(new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(s), (g) => g.segment);
  return [...s];
}

/**
 * A short label for an account in the collapsed sidebar: the first letters
 * of the first two words, or the first two characters of a single word.
 */
export function railLabel(name: string): string {
  const words = name.split(separators).filter(Boolean);
  const chars = words.length >= 2 ? [graphemes(words[0]!)[0], graphemes(words[1]!)[0]] : graphemes(words[0] ?? "").slice(0, 2);
  return chars.map((c) => c?.toLocaleUpperCase() ?? "").join("");
}
