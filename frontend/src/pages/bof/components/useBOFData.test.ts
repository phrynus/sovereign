import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  getMock,
  delMock,
  postMock,
  postFormDataMock,
  postJsonMock,
  fetchAgentListCachedMock,
  toastErrorMock,
  sonnerErrorMock,
  tMock,
} = vi.hoisted(() => ({
  getMock: vi.fn(),
  delMock: vi.fn(),
  postMock: vi.fn(),
  postFormDataMock: vi.fn(),
  postJsonMock: vi.fn(),
  fetchAgentListCachedMock: vi.fn(),
  toastErrorMock: vi.fn(),
  sonnerErrorMock: vi.fn(),
  tMock: (key: string) => `T:${key}`,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    del: (...args: unknown[]) => delMock(...args),
    post: (...args: unknown[]) => postMock(...args),
    postFormData: (...args: unknown[]) => postFormDataMock(...args),
    postJson: (...args: unknown[]) => postJsonMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: (...args: unknown[]) => fetchAgentListCachedMock(...args),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: tMock }),
}));

vi.mock("@/lib/hooks/useErrorToast", () => ({
  useErrorToast: () => ({ toastError: toastErrorMock, warnError: vi.fn() }),
}));

vi.mock("sonner", () => ({
  toast: { error: sonnerErrorMock, success: vi.fn() },
}));

import { useBOFData } from "./useBOFData";

describe("useBOFData", () => {
  beforeEach(() => {
    getMock.mockReset();
    delMock.mockReset();
    postMock.mockReset();
    postFormDataMock.mockReset();
    postJsonMock.mockReset();
    fetchAgentListCachedMock.mockReset();
    toastErrorMock.mockReset();
    sonnerErrorMock.mockReset();
  });

  it("loads BOF files, executions, and agents", async () => {
    getMock
      .mockResolvedValueOnce({ bofs: [{ id: "1", name: "whoami", arch: "x64" }] })
      .mockResolvedValueOnce({ results: [{ id: 10, bof_name: "whoami", status: "completed" }] });
    fetchAgentListCachedMock.mockResolvedValueOnce([{ id: "agent-1", hostname: "WIN-01" }]);

    const { result } = renderHook(() => useBOFData());
    await waitFor(() => expect(result.current.loading).toBe(false));

    expect(result.current.loadError).toBeNull();
    expect(result.current.files).toHaveLength(1);
    expect(result.current.executions).toHaveLength(1);
    expect(result.current.agents).toEqual([{ id: "agent-1", hostname: "WIN-01" }]);
  });

  it("normalizes load errors via errorText and retains previous snapshot", async () => {
    getMock
      .mockResolvedValueOnce({ bofs: [{ id: "1", name: "whoami", arch: "x64" }] })
      .mockResolvedValueOnce({ results: [] });
    fetchAgentListCachedMock.mockResolvedValueOnce([]);

    const { result } = renderHook(() => useBOFData());
    await waitFor(() => expect(result.current.loading).toBe(false));

    getMock.mockRejectedValueOnce({ status: 403, message: "denied" });
    await act(async () => {
      await result.current.loadFiles();
    });

    expect(result.current.loadError).toBe("T:common.forbidden");
    expect(result.current.files).toHaveLength(1);
  });

  it("normalizes repo tab load errors", async () => {
    getMock
      .mockResolvedValueOnce({ bofs: [] })
      .mockResolvedValueOnce({ results: [] });
    fetchAgentListCachedMock.mockResolvedValueOnce([]);

    const { result } = renderHook(() => useBOFData());
    await waitFor(() => expect(result.current.loading).toBe(false));

    getMock.mockRejectedValueOnce({ status: 500, message: "boom" });
    await act(async () => {
      result.current.setActiveTab("repo");
    });

    await waitFor(() => expect(result.current.repoLoading).toBe(false));
    expect(result.current.repoError).toBe("T:common.server_error");
  });

  it("routes mutation failures through toastError with fallback keys", async () => {
    getMock.mockResolvedValue({ bofs: [], results: [] });
    fetchAgentListCachedMock.mockResolvedValue([]);

    const { result } = renderHook(() => useBOFData());
    await waitFor(() => expect(result.current.loading).toBe(false));

    const err = new Error("backend rejected");
    delMock.mockRejectedValueOnce(err);
    await act(async () => {
      await result.current.deleteBOF("bof-1");
    });
    expect(toastErrorMock).toHaveBeenCalledWith(err, "bof.toast.delete_failed");

    postMock.mockRejectedValueOnce(err);
    await act(async () => {
      await result.current.runBOF("bof-1", "agent-1", "arg");
    });
    expect(toastErrorMock).toHaveBeenCalledWith(err, "bof.toast.run_failed");

    postMock.mockRejectedValueOnce(err);
    await act(async () => {
      await result.current.editBOF("bof-1", "new-name", "desc");
    });
    expect(toastErrorMock).toHaveBeenCalledWith(err, "bof.toast.edit_failed");
  });
});
