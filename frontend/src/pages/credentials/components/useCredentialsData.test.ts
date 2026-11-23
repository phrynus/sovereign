import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCredentialsData } from "./useCredentialsData";

const { getMock, subscribeMock, toastErrorMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  subscribeMock: vi.fn(),
  toastErrorMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: { get: (...args: unknown[]) => getMock(...args) },
}));

vi.mock("@/lib/wsContext", () => ({
  useWS: () => ({ subscribe: subscribeMock }),
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  // Stable t(): a fresh function every render would re-create loadData and
  // re-trigger its effect forever.
  useI18n: () => ({ t: tMock }),
}));

vi.mock("sonner", () => ({
  toast: { error: toastErrorMock, success: vi.fn() },
}));

vi.mock("@/lib/api-paths", () => ({
  paths: { credentials: { list: () => "/api/credentials/list" } },
}));

const vault = (count: number) => ({ data: { credentials: Array.from({ length: count }, (_, i) => ({ username: `u${i}` })) } });

describe("useCredentialsData", () => {
  beforeEach(() => {
    getMock.mockReset();
    subscribeMock.mockReset();
    toastErrorMock.mockReset();
    subscribeMock.mockReturnValue(() => {});
  });

  it("shows the loading state only until the first load lands", async () => {
    getMock.mockResolvedValue(vault(2));
    const { result } = renderHook(() => useCredentialsData());

    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.data).not.toBeNull();
  });

  it("keeps the loaded rows on screen when a WebSocket refresh fails", async () => {
    getMock.mockResolvedValueOnce(vault(3)).mockRejectedValueOnce(new Error("ws boom"));
    const { result } = renderHook(() => useCredentialsData());
    await waitFor(() => expect(result.current.loading).toBe(false));
    const loaded = result.current.data;
    expect(loaded).not.toBeNull();

    await act(async () => {
      await result.current.loadData();
    });

    // A failed background refresh reports the error but does not blank the vault
    // into a fake "no credentials" state.
    expect(result.current.error).toBe("ws boom");
    expect(result.current.data).toBe(loaded);
    expect(toastErrorMock).toHaveBeenCalledWith("ws boom");
  });

  it("never re-enters the loading state once data exists", async () => {
    getMock.mockResolvedValue(vault(1));
    const { result } = renderHook(() => useCredentialsData());
    await waitFor(() => expect(result.current.loading).toBe(false));

    let sawLoading = true;
    await act(async () => {
      const pending = result.current.loadData();
      sawLoading = result.current.loading;
      await pending;
    });

    expect(sawLoading).toBe(false);
  });
});

describe("useCredentialsData WebSocket throttle", () => {
  it("coalesces a harvest burst into one immediate and one trailing refresh", async () => {
    vi.useFakeTimers();
    try {
      getMock.mockResolvedValue(vault(2));
      let subscriber: ((msg: { type: string }) => void) | undefined;
      subscribeMock.mockImplementation((fn: (msg: { type: string }) => void) => {
        subscriber = fn;
        return () => {};
      });
      const { unmount } = renderHook(() => useCredentialsData());
      await act(async () => {
        await Promise.resolve();
      });
      getMock.mockClear();

      act(() => {
        subscriber?.({ type: "credential_update" });
        subscriber?.({ type: "credential_update" });
        subscriber?.({ type: "sync" });
      });
      expect(getMock).toHaveBeenCalledTimes(1);

      // Let the immediate load settle so its in-flight join does not swallow
      // the trailing refresh, then advance past the throttle window.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(800);
      });
      expect(getMock).toHaveBeenCalledTimes(2);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a pending trailing refresh on unmount", async () => {
    vi.useFakeTimers();
    try {
      getMock.mockResolvedValue(vault(1));
      let subscriber: ((msg: { type: string }) => void) | undefined;
      subscribeMock.mockImplementation((fn: (msg: { type: string }) => void) => {
        subscriber = fn;
        return () => {};
      });
      const { unmount } = renderHook(() => useCredentialsData());
      await act(async () => {
        await Promise.resolve();
      });
      getMock.mockClear();

      act(() => {
        subscriber?.({ type: "credential_update" });
        subscriber?.({ type: "credential_update" });
      });
      expect(getMock).toHaveBeenCalledTimes(1);
      unmount();
      vi.advanceTimersByTime(2000);
      expect(getMock).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
