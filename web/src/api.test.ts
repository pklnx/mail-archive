import { describe, expect, it } from "vitest";
import { ApiError, folderQuestion } from "./api";

describe("folderQuestion", () => {
  it("returns the folders the server asks about", () => {
    const err = new ApiError(409, "confirm", { error: "confirm", suggestedExclusions: ["Trash", "Spam"] });
    expect(folderQuestion(err)).toEqual(["Trash", "Spam"]);
  });
  it("ignores other errors", () => {
    expect(folderQuestion(new ApiError(409, "an account named x already exists", { error: "…" }))).toBeNull();
    expect(folderQuestion(new ApiError(422, "login failed", { suggestedExclusions: ["Trash"] }))).toBeNull();
    expect(folderQuestion(new ApiError(409, "x", { suggestedExclusions: [1] }))).toBeNull();
    expect(folderQuestion(new Error("network"))).toBeNull();
  });
});
