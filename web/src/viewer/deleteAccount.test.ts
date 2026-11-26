import { describe, expect, it } from "vitest";
import { deletionText } from "./pages/deleteAccount";

describe("deletionText", () => {
  it("says which way the household goes before anyone confirms", () => {
    expect(deletionText({ outcome: "account", kidNames: [] })).toMatch(
      /cannot be undone\. The household stays as it is\.$/,
    );
    expect(deletionText({ outcome: "transfer", newOwnerName: "Bea", kidNames: [] })).toMatch(
      /The household stays, with its kids, and Bea becomes its owner\.$/,
    );
    expect(deletionText({ outcome: "household", kidNames: [] })).toMatch(
      /so the household goes too\.$/,
    );
    expect(deletionText({ outcome: "household", kidNames: ["Mia", "Leo", "Ada"] })).toMatch(
      /the household goes too, with Mia, Leo and Ada and everything of theirs\.$/,
    );
    expect(deletionText({ outcome: "household", kidNames: ["Mia", "Leo"] })).toContain(
      "with Mia and Leo and",
    );
  });

  it("tells the server's only admin to make another admin first", () => {
    const text = deletionText({ outcome: "refused", kidNames: [] });
    expect(text).toContain("make another account an admin");
    expect(text).not.toContain("cannot be undone");
  });
});
