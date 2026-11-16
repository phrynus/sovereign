import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { mapContainedPoint, useRemoteDesktop } from "./useRemoteDesktop";
import { api } from "@/lib/api";

type WSMessage = Record<string, unknown>;

const hooksMock = vi.hoisted(() => ({ cb: null as null | (() => void), delay: 0 }));
const wsMock = vi.hoisted(() => ({ handler: null as null | ((msg: WSMessage) => void) }));
const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));

vi.mock("@/lib/hooks/useVisibleInterval", () => ({
  useVisibleInterval: (cb: () => void, delay: number) => {
    hooksMock.cb = cb;
    hooksMock.delay = delay;
  },
}));

vi.mock("@/lib/wsContext", () => ({
  useWS: () => ({
    subscribe: (handler: (msg: WSMessage) => void) => {
      wsMock.handler = handler;
      return () => {
        wsMock.handler = null;
      };
    },
  }),
}));

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
  formatThrownError: (err: unknown) => (err instanceof Error ? err.message : String(err)),
}));

vi.mock("@/lib/i18n", () => {
  // `t` must be referentially stable: the hook's callbacks carry it.
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }) };
});

vi.mock("sonner", () => ({
  toast: { success: toastMock.success, error: toastMock.error },
}));

const mockedGet = vi.mocked(api.get);
const mockedPost = vi.mocked(api.post);

function screenshotCalls(): number {
  return mockedGet.mock.calls.filter((call) => String(call[0]).includes("/screenshot")).length;
}

async function mountStarted(agentId = "agent-1") {
  const hook = renderHook(() => useRemoteDesktop(agentId));
  // Let the version fetch resolve so startMonitoring is not version-gated.
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await hook.result.current.startMonitoring();
  });
  return hook;
}

beforeEach(() => {
  vi.clearAllMocks();
  wsMock.handler = null;
  hooksMock.cb = null;
  hooksMock.delay = 0;
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    cb(0);
    return 1;
  });
  mockedGet.mockImplementation(async (path: string) => {
    if (path === "/agents/agent-1") return { agent: { version: "3.5.0" } };
    return { image: "AAAA", width: 100, height: 50 };
  });
  mockedPost.mockResolvedValue({ success: true });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useRemoteDesktop hardening", () => {
  it("posts the stream interval in seconds, not milliseconds", async () => {
    await mountStarted();

    const startCall = mockedPost.mock.calls.find((call) => String(call[0]).includes("/screen/start"));
    expect(startCall).toBeDefined();
    // Default resolution is medium (1000ms), which the server only accepts as
    // "1"-second units; the raw ms value used to be rejected with 400.
    expect(startCall?.[1]).toEqual({ interval: "1" });
  });

  it("skips the HTTP fallback poll while WS frames are live, and resumes when they stop", async () => {
    const nowSpy = vi.spyOn(Date, "now");
    nowSpy.mockReturnValue(1_000_000);
    const { result } = await mountStarted();
    expect(result.current.monitoring).toBe(true);
    // The forced first capture after start goes out over HTTP.
    expect(screenshotCalls()).toBe(1);

    act(() => {
      wsMock.handler?.({ type: "screenshot", agent_id: "agent-1", data: "AAAA" });
    });

    // Fresh WS frame: the poll tick must not fire another HTTP request.
    await act(async () => {
      hooksMock.cb?.();
      await Promise.resolve();
    });
    expect(screenshotCalls()).toBe(1);

    // Once the WS stream is stale (10s > max(5s, 2.5 polls)), polling resumes.
    nowSpy.mockReturnValue(1_010_000);
    await act(async () => {
      hooksMock.cb?.();
      await Promise.resolve();
    });
    expect(screenshotCalls()).toBe(2);
    nowSpy.mockRestore();
  });

  it("resets the WS-live shortcut when monitoring stops", async () => {
    const nowSpy = vi.spyOn(Date, "now");
    nowSpy.mockReturnValue(2_000_000);
    const { result } = await mountStarted();
    act(() => {
      wsMock.handler?.({ type: "screenshot", agent_id: "agent-1", data: "AAAA" });
    });

    await act(async () => {
      await result.current.stopMonitoring();
    });
    expect(result.current.monitoring).toBe(false);

    await act(async () => {
      await result.current.startMonitoring();
    });
    // Without the reset, the stale timestamp would suppress the forced first
    // capture of the new session.
    expect(screenshotCalls()).toBe(2);
    nowSpy.mockRestore();
  });

  it("surfaces the backend text when the start is rejected", async () => {
    const hook = renderHook(() => useRemoteDesktop("agent-1"));
    await act(async () => {
      await Promise.resolve();
    });
    mockedPost.mockRejectedValue({ status: 400, message: "interval must be between 1 and 60 seconds" });

    await act(async () => {
      await hook.result.current.startMonitoring();
    });

    expect(toastMock.error).toHaveBeenCalledWith("interval must be between 1 and 60 seconds");
    expect(hook.result.current.status).toBe("error");
  });

  it("normalizes a rejected remote click", async () => {
    const { result } = await mountStarted();

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "input rejected" });
    await act(async () => {
      await result.current.handleClick({ clientX: 10, clientY: 20 } as React.MouseEvent<HTMLDivElement>);
    });

    expect(toastMock.error).toHaveBeenCalledWith("input rejected");
  });
});

describe("mapContainedPoint", () => {
  // A 1280x720 source rendered into a 1000x1000 stage: 1000x562.5 with 218.75
  // px bars above and below.
  const stage = { left: 0, top: 0, width: 1000, height: 1000 };

  it("maps the center of a letterboxed video to the center of the source", () => {
    expect(mapContainedPoint(500, 500, stage, 1280, 720)).toEqual({ x: 640, y: 360 });
  });

  it("clamps a click on the black bars to the nearest source edge", () => {
    // 10 px from the top is inside the letterbox bar.
    expect(mapContainedPoint(500, 10, stage, 1280, 720).y).toBe(0);
    expect(mapContainedPoint(500, 990, stage, 1280, 720).y).toBe(719);
    // The left/right bars are absent here, so x still maps across the width.
    expect(mapContainedPoint(10, 500, stage, 1280, 720).x).toBe(13);
  });

  it("offsets by the element position and never exceeds the source", () => {
    const offset = { left: 100, top: 50, width: 1000, height: 1000 };
    expect(mapContainedPoint(600, 550, offset, 1280, 720)).toEqual({ x: 640, y: 360 });
    const far = mapContainedPoint(5000, 5000, offset, 1280, 720);
    expect(far.x).toBe(1279);
    expect(far.y).toBe(719);
  });

  it("returns the origin for a zero-sized source instead of dividing by zero", () => {
    expect(mapContainedPoint(10, 10, stage, 0, 0)).toEqual({ x: 0, y: 0 });
  });
});
