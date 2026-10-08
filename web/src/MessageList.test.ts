import { describe, expect, it } from "vitest";
import type { MessageSummary } from "./api";
import { earlierMembers } from "./MessageList";

const msg = (id: string): MessageSummary => ({ id, size: 1, subject: id, from: "", sentAt: null, sortAt: "2026-10-01T00:00:00Z", hasAttachment: false });

describe("earlierMembers", () => {
  it("leaves out the newest message, which the grouped row shows", () => {
    expect(earlierMembers([msg("c"), msg("b"), msg("a")], "c").map((m) => m.id)).toEqual(["b", "a"]);
    // A newer match that arrived meanwhile stays; only the row's message goes.
    expect(earlierMembers([msg("d"), msg("c"), msg("b")], "c").map((m) => m.id)).toEqual(["d", "b"]);
  });
});
