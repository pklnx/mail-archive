import { describe, expect, it } from "vitest";
import { dedicatedDomain, postfixSetup, regexpQuote } from "./journalSetup";

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
    const s = postfixSetup(["journal@example.com", "copies@mail-archive.internal"], "[10.0.0.5]:25");
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
