import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { api, pollTask } from "@/lib/api";
import { useFileEditor } from "./useFileEditor";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn() },
  pollTask: vi.fn(),
  getRateLimitRetryAfter: vi.fn(() => null),
}));

const t = (key: string) => key;
const READ_TASK = 11;
const WRITE_TASK = 12;
const HASH = "a".repeat(64);

function editor() {
  return renderHook(() => useFileEditor({ agentId: "a1", path: "C:\\note.txt", t }));
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockResolvedValue({ revisions: [] });
  vi.mocked(api.post).mockImplementation(async (path: string) => {
    if (String(path).includes("/files/write")) return { task_id: WRITE_TASK };
    return { task_id: READ_TASK };
  });
  vi.mocked(pollTask).mockImplementation(async (_agent, taskId) => {
    if (taskId === READ_TASK) {
      return {
        task_id: READ_TASK, type: "read", status: "completed",
        result: "old text", result_sha256: HASH, result_mtime: 123,
      } as never;
    }
    return {
      task_id: WRITE_TASK, type: "write", status: "completed",
      result: "", result_sha256: "b".repeat(64), result_mtime: 124,
    } as never;
  });
});

describe("useFileEditor", () => {
  it("opens a read baseline and tracks dirty state", async () => {
    const { result } = editor();
    await act(async () => { await result.current.open(); });
    expect(result.current.baselineTaskId).toBe(READ_TASK);
    expect(result.current.baselineHash).toBe(HASH);
    expect(result.current.draft).toBe("old text");
    expect(result.current.dirty).toBe(false);

    act(() => result.current.changeDraft("new text"));
    expect(result.current.dirty).toBe(true);
  });

  it("saves base64 content with the baseline task id", async () => {
    const { result } = editor();
    await act(async () => { await result.current.open(); });
    await act(async () => { result.current.changeDraft("hello"); });
    await act(async () => { await result.current.save(); });

    const writeCall = vi.mocked(api.post).mock.calls.find(([p]) => String(p).includes("/files/write"));
    expect(writeCall?.[1]).toMatchObject({
      path: "C:\\note.txt",
      encoding: "base64",
      base_task_id: String(READ_TASK),
    });
    expect((writeCall?.[1] as { content: string }).content).toBe(btoa("hello"));
    await waitFor(() => expect(result.current.dirty).toBe(false));
    expect(result.current.baselineTaskId).toBe(WRITE_TASK);
  });

  it("keeps the draft and flags conflict on file_conflict", async () => {
    const { result } = editor();
    await act(async () => { await result.current.open(); });
    act(() => result.current.changeDraft("my draft"));
    vi.mocked(pollTask).mockImplementation(async (_agent, taskId) => {
      if (taskId === READ_TASK) {
        return { task_id: READ_TASK, status: "completed", result: "old text", result_sha256: HASH } as never;
      }
      return { task_id: WRITE_TASK, status: "failed", error: "file_conflict: external edit" } as never;
    });
    await act(async () => { await expect(result.current.save()).rejects.toThrow(/file_conflict/); });
    expect(result.current.conflict).toBe(true);
    expect(result.current.draft).toBe("my draft");
  });

  it("overwrite rebases on a fresh hash before saving the retained draft", async () => {
    const { result } = editor();
    await act(async () => { await result.current.open(); });
    act(() => result.current.changeDraft("kept draft"));
    vi.mocked(pollTask).mockImplementation(async (_agent, taskId) => {
      if (taskId === READ_TASK) {
        return {
          task_id: READ_TASK, status: "completed", result: "remote changed",
          result_sha256: "c".repeat(64),
        } as never;
      }
      return { task_id: WRITE_TASK, status: "completed", result: "", result_sha256: "d".repeat(64) } as never;
    });
    await act(async () => { await result.current.overwrite(); });
    const writeCall = vi.mocked(api.post).mock.calls.find(([p]) => String(p).includes("/files/write"));
    expect(writeCall?.[1]).toMatchObject({ base_task_id: String(READ_TASK) });
    expect(result.current.baselineHash).toBe("d".repeat(64));
  });

  it("loads read-only revision snapshots", async () => {
    const { result } = editor();
    vi.mocked(api.get).mockResolvedValueOnce({ content_base64: btoa("older text") });
    await act(async () => { await result.current.viewRevision(7); });
    expect(result.current.revisionText).toBe("older text");
  });
});
