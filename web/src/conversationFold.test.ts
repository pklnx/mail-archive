// The fold of the conversation in the message view (#200).
import { describe, expect, it } from "vitest";
import { foldConversation } from "./conversationFold";
import { applySettings, messagesFor } from "./i18n";

/** n messages with the ids 1..n, oldest first. */
const thread = (n: number) => Array.from({ length: n }, (_, i) => ({ id: String(i + 1) }));
const ids = (ms: { id: string }[]) => ms.map((m) => m.id);
const fold = (n: number, open: string) => {
  const f = foldConversation(thread(n), open);
  return { earlier: ids(f.earlier), visible: ids(f.visible), later: ids(f.later) };
};

describe("foldConversation", () => {
  it("shows conversations of up to 3 messages in full, wherever the open one is", () => {
    for (const open of ["1", "2"]) expect(fold(2, open)).toEqual({ earlier: [], visible: ["1", "2"], later: [] });
    for (const open of ["1", "2", "3", "absent"]) expect(fold(3, open)).toEqual({ earlier: [], visible: ["1", "2", "3"], later: [] });
  });

  it("shows the open message and one neighbour on each side from 4 messages on", () => {
    expect(fold(4, "2")).toEqual({ earlier: [], visible: ["1", "2", "3"], later: ["4"] });
    expect(fold(4, "3")).toEqual({ earlier: ["1"], visible: ["2", "3", "4"], later: [] });
    expect(fold(6, "4")).toEqual({ earlier: ["1", "2"], visible: ["3", "4", "5"], later: ["6"] });
    expect(fold(20, "10")).toEqual({
      earlier: ["1", "2", "3", "4", "5", "6", "7", "8"],
      visible: ["9", "10", "11"],
      later: ["12", "13", "14", "15", "16", "17", "18", "19", "20"],
    });
  });

  it("has only one neighbour for the first and the last message", () => {
    expect(fold(4, "1")).toEqual({ earlier: [], visible: ["1", "2"], later: ["3", "4"] });
    expect(fold(4, "4")).toEqual({ earlier: ["1", "2"], visible: ["3", "4"], later: [] });
    expect(fold(20, "1")).toEqual({ earlier: [], visible: ["1", "2"], later: ids(thread(20)).slice(2) });
    expect(fold(20, "20")).toEqual({ earlier: ids(thread(20)).slice(0, 18), visible: ["19", "20"], later: [] });
  });

  it("shows the newest message when the open one is not among them (more than the server returns)", () => {
    expect(fold(4, "absent")).toEqual({ earlier: ["1", "2", "3"], visible: ["4"], later: [] });
    expect(fold(20, "absent")).toEqual({ earlier: ids(thread(20)).slice(0, 19), visible: ["20"], later: [] });
  });

  it("keeps every message exactly once, in order", () => {
    for (const n of [2, 3, 4, 5, 20]) {
      for (const open of [...ids(thread(n)), "absent"]) {
        const f = fold(n, open);
        expect([...f.earlier, ...f.visible, ...f.later]).toEqual(ids(thread(n)));
      }
    }
  });

  it("counts match the toggle labels", () => {
    applySettings({ language: "en", locale: "en" });
    const en = messagesFor("en");
    const f = foldConversation(thread(6), "4");
    expect(en.showConversation(f.earlier.length)).toBe("Show 2 earlier messages");
    expect(en.showLaterMessages(f.later.length)).toBe("Show 1 later message");
    const de = messagesFor("de");
    expect(de.showConversation(f.earlier.length)).toBe("2 ältere Nachrichten anzeigen");
    expect(de.showLaterMessages(f.later.length)).toBe("1 neuere Nachricht anzeigen");
    expect(de.showLaterMessages(3)).toBe("3 neuere Nachrichten anzeigen");
  });
});
