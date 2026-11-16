import { describe, expect, it } from "vitest";
import { CLIPBOARD_PREVIEW_CHARS, clipboardPreview, historyTime } from "./RdpClipboard";

describe("clipboardPreview", () => {
  it("collapses newlines and surrounding whitespace into one line", () => {
    expect(clipboardPreview("  line one\nline two\t\n")).toBe("line one line two");
  });

  it("elides a long value instead of rendering the whole clipboard", () => {
    const long = "a".repeat(CLIPBOARD_PREVIEW_CHARS + 40);
    const preview = clipboardPreview(long);
    expect(preview.endsWith("…")).toBe(true);
    expect(Array.from(preview)).toHaveLength(CLIPBOARD_PREVIEW_CHARS + 1);
  });

  it("keeps a short value intact", () => {
    expect(clipboardPreview("whoami")).toBe("whoami");
    expect(clipboardPreview("")).toBe("");
  });

  it("counts characters, not UTF-16 units, when eliding", () => {
    const emoji = "🙂".repeat(CLIPBOARD_PREVIEW_CHARS + 5);
    const preview = clipboardPreview(emoji);
    // No lone surrogate: every code point stays a whole emoji.
    expect(preview).not.toMatch(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/);
    expect(Array.from(preview)).toHaveLength(CLIPBOARD_PREVIEW_CHARS + 1);
  });
});

describe("historyTime", () => {
  it("renders a local HH:MM:SS label from an RFC3339 timestamp", () => {
    const iso = new Date(2026, 10, 16, 9, 5, 3).toISOString();
    expect(historyTime(iso)).toBe("09:05:03");
  });

  it("returns an empty label for an unusable timestamp", () => {
    expect(historyTime("")).toBe("");
    expect(historyTime("not-a-date")).toBe("");
  });
});
