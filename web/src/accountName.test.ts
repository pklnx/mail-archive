import { describe, expect, it } from "vitest";
import { suggestName } from "./accountName";

describe("suggestName", () => {
  it("uses the domain of the login", () => {
    expect(suggestName("postmaster@pklnx.space", "")).toBe("pklnx");
    expect(suggestName("me@gmail.com", "imap.gmail.com")).toBe("gmail");
    expect(suggestName("x@mail.de", "imap.mail.de")).toBe("mail");
    expect(suggestName("a@example.co.uk", "")).toBe("example");
    expect(suggestName("b@Sub.Example.ORG", "")).toBe("example");
  });
  it("falls back to the server without generic labels", () => {
    expect(suggestName("alice", "imap.gmail.com")).toBe("gmail");
    expect(suggestName("alice", "imap.mail.me.com")).toBe("");
    expect(suggestName("alice", "mail.pklnx.space")).toBe("pklnx");
    expect(suggestName("alice", "imap.gmx.net")).toBe("gmx");
  });
  it("suggests nothing it cannot derive", () => {
    expect(suggestName("", "")).toBe("");
    expect(suggestName("alice", "127.0.0.1")).toBe("");
    expect(suggestName("alice", "::1")).toBe("");
    expect(suggestName("alice@localhost", "")).toBe("");
  });
});
