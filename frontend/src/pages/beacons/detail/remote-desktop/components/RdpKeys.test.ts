import { describe, expect, it } from "vitest";
import { KEY_FEED_MAX_CHARS, appendKeyFeed } from "./RdpKeys";

describe("appendKeyFeed", () => {
  it("appends a delta and ignores an empty one", () => {
    expect(appendKeyFeed("who", "ami")).toBe("whoami");
    expect(appendKeyFeed("who", "")).toBe("who");
  });

  it("keeps only the newest characters once the cap is reached", () => {
    const start = "a".repeat(KEY_FEED_MAX_CHARS);
    const next = appendKeyFeed(start, "TAIL");
    expect(next).toHaveLength(KEY_FEED_MAX_CHARS);
    expect(next.endsWith("TAIL")).toBe(true);
  });

  it("drops history rather than freezing the feed", () => {
    // A delta larger than the cap must still show what was just typed.
    const delta = "x".repeat(KEY_FEED_MAX_CHARS + 100) + "LATEST";
    const next = appendKeyFeed("", delta, 1000);
    expect(next).toHaveLength(1000);
    expect(next.endsWith("LATEST")).toBe(true);
  });
});
