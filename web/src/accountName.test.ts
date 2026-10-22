import { describe, expect, it } from "vitest";
import { graphemes, railLabel, suggestName } from "./accountName";

describe("railLabel", () => {
  it("takes the first letters of two words or of one word", () => {
    expect(railLabel("work")).toBe("WO");
    expect(railLabel("Private Mail")).toBe("PM");
    expect(railLabel("max.mustermann@example.org")).toBe("MM");
    expect(railLabel("a")).toBe("A");
    expect(railLabel("ä-büro")).toBe("ÄB");
    expect(railLabel("")).toBe("");
  });
  it("keeps emoji and flags whole", () => {
    const family = "\u{1F468}‍\u{1F469}‍\u{1F467}";
    expect(railLabel(`${family} home`)).toBe(`${family}H`);
    expect(railLabel(`${family}x`)).toBe(`${family}X`);
    expect(railLabel("\u{1F1E9}\u{1F1EA}")).toBe("\u{1F1E9}\u{1F1EA}");
  });
  it("falls back to code points without Intl.Segmenter", () => {
    expect(graphemes("a\u{1F600}b", false)).toEqual(["a", "\u{1F600}", "b"]);
    expect(graphemes("éx", true)).toEqual(["é", "x"]);
  });
});

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
