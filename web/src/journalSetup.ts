// The Postfix lines an smtp account needs, from the journaling guide,
// filled in with one of its addresses.

/** Where Postfix reaches the archive with the guide's Docker Compose setup (SMTP_BIND, SMTP_PORT). */
export const defaultTarget = "[127.0.0.1]:2525";

/** The guide's name and port of a hosted archive, where mail servers log in (KEEPIA_ARCHIVE_SMTP_AUTH=required). */
export const hostedTarget = "[archive.customer.example]:587";

/** Stands for the secret in the lines: it is shown only once, when it is created. */
export const secretPlaceholder = "mas_…";

/** Stands for the user name where it is not known (the owner is no admin). */
export const usernamePlaceholder = "smtp-…";

/** The line of Postfix's password map (/etc/postfix/archive_sasl) for a login. */
export function saslLine(username: string, secret = secretPlaceholder, target = hostedTarget): string {
  return `${target}   ${username}:${secret}`;
}

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
 * With a user name (KEEPIA_ARCHIVE_SMTP_AUTH=required), the copies go over a
 * transport of their own that logs in with it over verified TLS, as in the
 * guide's "Log in with an SMTP secret"; without one (off), over plain smtp.
 */
export function postfixSetup(addresses: readonly string[], username: string | null = null, target = username === null ? defaultTarget : hostedTarget): PostfixSetup {
  const own = addresses.find((a) => dedicatedDomain(a.slice(a.lastIndexOf("@") + 1)));
  const address = own ?? guideAddress;
  const at = address.lastIndexOf("@");
  const local = address.slice(0, at);
  const domain = address.slice(at + 1);
  const route =
    username === null
      ? [
          "# /etc/postfix/transport",
          `${domain}   smtp:${target}`,
          "",
          "postmap /etc/postfix/transport",
          "postconf -e 'transport_maps = hash:/etc/postfix/transport'",
        ]
      : [
          "# /etc/postfix/master.cf",
          "archive   unix  -  -  n  -  2  smtp",
          "  -o smtp_sasl_auth_enable=yes",
          "  -o smtp_sasl_password_maps=hash:/etc/postfix/archive_sasl",
          "  -o smtp_sasl_security_options=noanonymous",
          "  -o smtp_sasl_tls_security_options=noanonymous",
          "  -o smtp_tls_security_level=secure",
          "",
          "# /etc/postfix/transport",
          `${domain}   archive:${target}`,
          "",
          "# /etc/postfix/archive_sasl",
          saslLine(username, secretPlaceholder, target),
          "",
          "chmod 0600 /etc/postfix/archive_sasl",
          "postmap /etc/postfix/archive_sasl /etc/postfix/transport",
          "postconf -e 'transport_maps = hash:/etc/postfix/transport'",
        ];
  const text = [
    ...route,
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
