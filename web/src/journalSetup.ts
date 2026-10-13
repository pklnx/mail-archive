// The Postfix lines an smtp account needs, from the journaling guide,
// filled in with one of its addresses.

/** Where Postfix reaches the archive with the guide's Docker Compose setup (SMTP_BIND, SMTP_PORT). */
export const defaultTarget = "[127.0.0.1]:2525";

/** The guide's address, in a domain only the archive receives. */
export const guideAddress = "archive@archive.local";

/**
 * Whether mail to this domain can go to the archive without taking a real
 * domain's mail with it: the guide's archive.local, or a name under a
 * suffix that is never a public domain.
 */
export function dedicatedDomain(domain: string): boolean {
  const d = domain.toLowerCase();
  return [".local", ".internal", ".invalid", ".test", ".localhost"].some((s) => d.endsWith(s));
}

/** A string as a literal inside a Postfix regexp. */
export function regexpQuote(s: string): string {
  return s.replace(/[\\^$.*+?()[\]{}|/]/g, "\\$&");
}

export interface PostfixSetup {
  /** The address the lines use. */
  address: string;
  /** False when no address of the account is in a dedicated domain: the lines then use guideAddress. */
  dedicated: boolean;
  /** The transport map, the recipient_bcc_maps file and the commands. */
  text: string;
}

/**
 * The Postfix setup for an smtp account (variant B of the guide). The
 * transport line routes a whole domain to the archive, so it only uses an
 * address in a dedicated domain; otherwise the guide's archive.local.
 */
export function postfixSetup(addresses: readonly string[], target = defaultTarget): PostfixSetup {
  const own = addresses.find((a) => dedicatedDomain(a.slice(a.lastIndexOf("@") + 1)));
  const address = own ?? guideAddress;
  const at = address.lastIndexOf("@");
  const local = address.slice(0, at);
  const domain = address.slice(at + 1);
  const text = [
    "# /etc/postfix/transport",
    `${domain}   smtp:${target}`,
    "",
    "postmap /etc/postfix/transport",
    "postconf -e 'transport_maps = hash:/etc/postfix/transport'",
    "",
    "# /etc/postfix/archive_bcc (regexp)",
    `if !/@${regexpQuote(domain)}$/`,
    `/^(.+)@(.+)$/   ${local}+\${1}=\${2}@${domain}`,
    "endif",
    "",
    "postconf -e 'recipient_bcc_maps = regexp:/etc/postfix/archive_bcc'",
    "postfix reload",
  ].join("\n");
  return { address, dedicated: own !== undefined, text };
}
