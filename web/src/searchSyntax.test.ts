import { afterEach, describe, expect, it, vi } from "vitest";
import { hasPrefix, localMidnight, parseQuery, searchFilter, setToken } from "./searchSyntax";

const empty = { text: "", from: "", to: "", attachment: "", hasAttachment: false, after: "", before: "" };

describe("parseQuery", () => {
  it("separates prefixes from free text", () => {
    expect(parseQuery("to:finanzamt after:2024-01-01 steuer bescheid")).toEqual({
      ...empty,
      text: "steuer bescheid",
      to: "finanzamt",
      after: "2024-01-01",
    });
    expect(parseQuery("FROM:Bob Has:Attachment attachment:rechnung before:2025-01-01")).toEqual({
      ...empty,
      from: "Bob",
      attachment: "rechnung",
      hasAttachment: true,
      before: "2025-01-01",
    });
    expect(parseQuery("")).toEqual(empty);
  });

  it("reads quoted values, also unclosed ones", () => {
    expect(parseQuery('to:"Finanzamt Köln" "genaue Phrase"')).toEqual({
      ...empty,
      to: "Finanzamt Köln",
      text: '"genaue Phrase"',
    });
    expect(parseQuery('to:"Finanzamt Kö').to).toBe("Finanzamt Kö");
  });

  it("lets the last occurrence win", () => {
    expect(parseQuery("to:a steuer to:b").to).toBe("b");
    expect(parseQuery("after:2024-01-01 after:2023-01-01").after).toBe("2023-01-01");
  });

  it("keeps everything else as text", () => {
    expect(parseQuery("subject:rechnung").text).toBe("subject:rechnung");
    expect(parseQuery("after:2024-13-01 before:gestern").text).toBe("after:2024-13-01 before:gestern");
    expect(parseQuery("after:2023-02-29").text).toBe("after:2023-02-29");
    expect(parseQuery("has:pdf").text).toBe("has:pdf");
    expect(parseQuery("https://example.com/x").text).toBe("https://example.com/x");
    expect(parseQuery("to://example.com").text).toBe("to://example.com");
    expect(parseQuery("-roadmap meeting").text).toBe("-roadmap meeting");
  });

  it("ignores a prefix without a value", () => {
    expect(parseQuery("steuer to:")).toEqual({ ...empty, text: "steuer" });
    expect(hasPrefix("steuer to:")).toBe(true);
    expect(hasPrefix("steuer subject:x")).toBe(false);
  });
});

describe("setToken", () => {
  it("adds, replaces and removes tokens", () => {
    expect(setToken("steuer", "to", "finanzamt")).toBe("steuer to:finanzamt");
    expect(setToken("to:a steuer", "to", "b")).toBe("to:b steuer");
    expect(setToken("to:a steuer to:c", "to", "b")).toBe("to:b steuer");
    expect(setToken("to:a steuer", "to", "")).toBe("steuer");
    expect(setToken("steuer", "has", "yes")).toBe("steuer has:attachment");
    expect(setToken("has:attachment steuer", "has", "")).toBe("steuer");
    expect(setToken("", "to", "Finanzamt Köln")).toBe('to:"Finanzamt Köln"');
    expect(setToken("", "to", 'a"b')).toBe("to:ab");
    expect(setToken("after:2024-13-01", "after", "2024-01-01")).toBe("after:2024-13-01 after:2024-01-01");
  });

  it("round-trips through parseQuery", () => {
    let q = "steuer";
    q = setToken(q, "to", "Finanzamt Köln ");
    q = setToken(q, "from", "bob");
    q = setToken(q, "after", "2024-01-01");
    q = setToken(q, "before", "2025-01-01");
    q = setToken(q, "has", "attachment");
    q = setToken(q, "attachment", "rechnung");
    expect(parseQuery(q)).toEqual({
      text: "steuer",
      to: "Finanzamt Köln ",
      from: "bob",
      attachment: "rechnung",
      hasAttachment: true,
      after: "2024-01-01",
      before: "2025-01-01",
    });
    for (const key of ["to", "from", "after", "before", "has", "attachment"] as const) q = setToken(q, key, "");
    expect(q).toBe("steuer");
  });
});

// Tests that set TZ restore it, so other tests run in the original zone.
afterEach(() => {
  vi.unstubAllEnvs();
});

describe("localMidnight", () => {
  it("uses the browser's offset", () => {
    vi.stubEnv("TZ", "Europe/Berlin");
    expect(localMidnight("2024-01-01")).toBe("2024-01-01T00:00:00+01:00");
    expect(localMidnight("2024-07-01")).toBe("2024-07-01T00:00:00+02:00");
    vi.stubEnv("TZ", "America/New_York");
    expect(localMidnight("2024-01-01")).toBe("2024-01-01T00:00:00-05:00");
    vi.stubEnv("TZ", "Asia/Kolkata");
    expect(localMidnight("2024-01-01")).toBe("2024-01-01T00:00:00+05:30");
    vi.stubEnv("TZ", "UTC");
    expect(localMidnight("2024-01-01")).toBe("2024-01-01T00:00:00+00:00");
  });
});

describe("searchFilter", () => {
  it("turns the search field into API parameters", () => {
    vi.stubEnv("TZ", "Europe/Berlin");
    expect(searchFilter('to:"Finanzamt " after:2024-01-01 has:attachment steuer')).toEqual({
      q: "steuer",
      from: "",
      to: "Finanzamt",
      attachment: "",
      hasAttachment: true,
      after: "2024-01-01T00:00:00+01:00",
      before: "",
    });
  });
});
