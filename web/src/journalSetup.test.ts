import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { dedicatedDomain, postfixSetup, regexpQuote, saslLine } from "./journalSetup";

/** The journaling guide's code blocks, each as its lines. */
function guideBlocks(): string[][] {
  const guide = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../../docs/guide/journaling.md"), "utf8");
  return [...guide.matchAll(/```(?:text|sh)\n([\s\S]*?)```/g)].map((m) => m[1]!.trimEnd().split("\n"));
}

describe("postfixSetup", () => {
  it("fills in the guide's lines for a dedicated domain", () => {
    const s = postfixSetup(["archive@archive.local"]);
    expect(s.dedicated).toBe(true);
    expect(s.text).toBe(
      [
        "# /etc/postfix/transport",
        "archive.local   smtp:[127.0.0.1]:2525",
        "",
        "postmap /etc/postfix/transport",
        "postconf -e 'transport_maps = hash:/etc/postfix/transport'",
        "",
        "# /etc/postfix/archive_bcc (regexp)",
        "if !/@archive\\.local$/",
        "/^(.+)@(.+)$/   archive+${1}=${2}@archive.local",
        "endif",
        "",
        "postconf -e 'recipient_bcc_maps = regexp:/etc/postfix/archive_bcc'",
        "postfix reload",
      ].join("\n"),
    );
  });
  it("uses the first address in a dedicated domain, with its local part", () => {
    const s = postfixSetup(["journal@example.com", "copies@mail-archive.internal"], null, "[10.0.0.5]:25");
    expect(s.address).toBe("copies@mail-archive.internal");
    expect(s.text).toContain("mail-archive.internal   smtp:[10.0.0.5]:25");
    expect(s.text).toContain("if !/@mail-archive\\.internal$/");
    expect(s.text).toContain("copies+${1}=${2}@mail-archive.internal");
  });
  it("never routes a real domain to the archive", () => {
    const s = postfixSetup(["journal@example.com"]);
    expect(s.dedicated).toBe(false);
    expect(s.address).toBe("archive@archive.local");
    expect(s.text).not.toContain("example.com");
  });
});

describe("postfixSetup with a login (#395)", () => {
  it("fills in the guide's transport, password map and user name", () => {
    const s = postfixSetup(["archive@archive.local"], "smtp-42");
    expect(s.text).toBe(
      [
        "# /etc/postfix/master.cf",
        "archive   unix  -  -  n  -  2  smtp",
        "  -o smtp_sasl_auth_enable=yes",
        "  -o smtp_sasl_password_maps=hash:/etc/postfix/archive_sasl",
        "  -o smtp_sasl_security_options=noanonymous",
        "  -o smtp_sasl_tls_security_options=noanonymous",
        "  -o smtp_tls_security_level=secure",
        "",
        "# /etc/postfix/transport",
        "archive.local   archive:[archive.customer.example]:587",
        "",
        "# /etc/postfix/archive_sasl",
        "[archive.customer.example]:587   smtp-42:mas_…",
        "",
        "chmod 0600 /etc/postfix/archive_sasl",
        "postmap /etc/postfix/archive_sasl /etc/postfix/transport",
        "postconf -e 'transport_maps = hash:/etc/postfix/transport'",
        "",
        "# /etc/postfix/archive_bcc (regexp)",
        "if !/@archive\\.local$/",
        "/^(.+)@(.+)$/   archive+${1}=${2}@archive.local",
        "endif",
        "",
        "postconf -e 'recipient_bcc_maps = regexp:/etc/postfix/archive_bcc'",
        "postfix reload",
      ].join("\n"),
    );
    expect(saslLine("smtp-42", "mas_x")).toBe("[archive.customer.example]:587   smtp-42:mas_x");
  });

  it("matches the guide's section Log in with an SMTP secret", () => {
    // Every block of the login lines is a block of the guide, line for line.
    const blocks = guideBlocks();
    const has = (lines: string[]) => blocks.some((b) => b.join("\n") === lines.join("\n"));
    const text = postfixSetup(["archive@archive.local"], "smtp-42").text.split("\n");
    const block = (first: string, n: number) => text.slice(text.indexOf(first), text.indexOf(first) + n);
    expect(has(block("# /etc/postfix/master.cf", 7))).toBe(true);
    expect(has(block("# /etc/postfix/transport", 2))).toBe(true);
    expect(has(block("# /etc/postfix/archive_sasl", 2))).toBe(true);
    expect(has(block("chmod 0600 /etc/postfix/archive_sasl", 3).slice(0, 2).concat("postfix reload"))).toBe(true);
  });
});

describe("helpers", () => {
  it("knows dedicated domains", () => {
    for (const d of ["archive.local", "ARCHIVE.LOCAL", "x.internal", "a.test", "b.invalid", "c.localhost"]) expect(dedicatedDomain(d)).toBe(true);
    for (const d of ["example.com", "local", "archive.local.example.com", "internal.example"]) expect(dedicatedDomain(d)).toBe(false);
  });
  it("quotes regexp characters", () => {
    expect(regexpQuote("a.b-c+d")).toBe("a\\.b-c\\+d");
    expect(regexpQuote("x/y$")).toBe("x\\/y\\$");
  });
});
