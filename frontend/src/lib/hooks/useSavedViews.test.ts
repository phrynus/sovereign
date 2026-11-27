import { renderHook, waitFor, act } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useSavedViews } from "./useSavedViews";

const { getMock, postJsonMock, delMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postJsonMock: vi.fn(),
  delMock: vi.fn(),
  tMock: (key: string) => `T:${key}`,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    postJson: (...args: unknown[]) => postJsonMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: tMock }),
}));

describe("useSavedViews", () => {
  beforeEach(() => {
    getMock.mockReset();
    postJsonMock.mockReset();
    delMock.mockReset();
  });

  it("loads the view list", async () => {
    getMock.mockResolvedValueOnce({ views: [{ id: 1, page: "agents", name: "web", state: "{}" }] });

    const { result } = renderHook(() => useSavedViews("agents"));
    await waitFor(() => expect(result.current.loaded).toBe(true));

    expect(result.current.error).toBeNull();
    expect(result.current.views.map((v) => v.name)).toEqual(["web"]);
  });

  it("keeps the last known views when a later read fails, instead of emptying them", async () => {
    getMock.mockResolvedValueOnce({ views: [{ id: 1, page: "agents", name: "web", state: "{}" }] });

    const { result } = renderHook(() => useSavedViews("agents"));
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.views).toHaveLength(1);

    getMock.mockRejectedValueOnce(new Error("settings offline"));
    await act(async () => {
      await result.current.reload();
    });

    // Clearing the list is what made the picker look empty while its save
    // button still worked; a same-name save then overwrote the hidden view.
    expect(result.current.error).toBeTruthy();
    expect(result.current.views.map((v) => v.name)).toEqual(["web"]);
  });

  it("reports the failure when the first read fails", async () => {
    getMock.mockRejectedValueOnce(new Error("settings offline"));

    const { result } = renderHook(() => useSavedViews("agents"));
    await waitFor(() => expect(result.current.loaded).toBe(true));

    expect(result.current.error).toBe("settings offline");
    expect(result.current.views).toEqual([]);
  });

  it("normalizes structured HTTP errors through i18n", async () => {
    getMock.mockRejectedValueOnce({ status: 403, message: "forbidden" });

    const { result } = renderHook(() => useSavedViews("agents"));
    await waitFor(() => expect(result.current.loaded).toBe(true));

    expect(result.current.error).toBe("T:common.forbidden");
  });
});
