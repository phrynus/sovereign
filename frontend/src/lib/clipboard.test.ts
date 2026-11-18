import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { copyText } from "./clipboard";

describe("copyText", () => {
  beforeEach(() => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
    document.execCommand = vi.fn().mockReturnValue(true);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("uses the async clipboard API when available", async () => {
    expect(await copyText("hello")).toBe(true);
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("hello");
    expect(document.execCommand).not.toHaveBeenCalled();
  });

  it("falls back to execCommand when the async API rejects", async () => {
    (navigator.clipboard.writeText as unknown as ReturnType<typeof vi.fn>).mockRejectedValue(new Error("denied"));
    expect(await copyText("hello")).toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith("copy");
  });

  it("falls back to execCommand when there is no async API", async () => {
    vi.stubGlobal("navigator", { clipboard: undefined });
    expect(await copyText("hello")).toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith("copy");
  });

  it("returns false when both mechanisms fail", async () => {
    vi.stubGlobal("navigator", { clipboard: undefined });
    document.execCommand = vi.fn().mockReturnValue(false);
    expect(await copyText("hello")).toBe(false);
  });
});
