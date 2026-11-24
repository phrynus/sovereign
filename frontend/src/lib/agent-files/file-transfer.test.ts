import { beforeEach, describe, expect, it, vi } from "vitest";
import { paths } from "@/lib/api-paths";
import { TASK_HARD_TIMEOUT_MS } from "@/lib/task-poll";
import {
  FILE_TRANSFER_CHUNK_SIZE,
  FILE_TRANSFER_MAX_BYTES,
  transferProgressAt,
} from "./file-task";
import {
  cancelTransfer,
  getTransfer,
  listTransfers,
  pauseTransfer,
  pullRemoteFile,
  pushLocalFile,
  resumeTransfer,
  type TransferSession,
} from "./file-transfer";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  postJson: vi.fn(),
  putJson: vi.fn(),
  del: vi.fn(),
  downloadGet: vi.fn(),
  pollTask: vi.fn(),
  downloadBlob: vi.fn(),
  digest: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: mocks.get,
    postJson: mocks.postJson,
    putJson: mocks.putJson,
    del: mocks.del,
    downloadGet: mocks.downloadGet,
  },
  pollTask: mocks.pollTask,
}));
vi.mock("@/lib/download", () => ({ downloadBlob: mocks.downloadBlob }));

const t = vi.fn((key: string) => key);
const CHUNK = FILE_TRANSFER_CHUNK_SIZE;

function session(overrides: Partial<TransferSession> = {}): TransferSession {
  return {
    id: 7,
    direction: "push",
    remote_path: "/tmp/blob.bin",
    filename: "blob.bin",
    total_size: 2 * CHUNK + 10,
    committed_offset: 0,
    chunk_size: CHUNK,
    status: "active",
    ...overrides,
  };
}

function testFile(size: number, name = "blob.bin"): File {
  const file = new File([new Uint8Array(Math.min(size, FILE_TRANSFER_MAX_BYTES))], name, {
    type: "application/octet-stream",
  });
  if (file.size !== size) Object.defineProperty(file, "size", { value: size });
  return file;
}

/** Route list vs detail reads by path so tests can drive committed offsets. */
function mockTransfers(list: TransferSession[], detail: (id: number) => TransferSession) {
  mocks.get.mockImplementation((path: string) => {
    if (/\/transfers\/\d+$/.test(path)) {
      const id = Number(path.split("/").pop());
      return Promise.resolve({ transfer: detail(id) });
    }
    if (path.endsWith("/transfers")) return Promise.resolve({ transfers: list });
    return Promise.resolve({});
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("crypto", {
    subtle: { digest: mocks.digest.mockResolvedValue(new ArrayBuffer(32)) },
  });
  mocks.pollTask.mockResolvedValue({ status: "completed" });
  mocks.downloadGet.mockResolvedValue({ blob: new Blob(["x"]), filename: "saved.bin" });
  mocks.postJson.mockResolvedValue({ transfer: session(), task_id: 12 });
  mocks.putJson.mockResolvedValue({ task_id: 11, transfer: session() });
  mocks.del.mockResolvedValue({ transfer: session() });
  mockTransfers([], () => session());
});

describe("durable session actions", () => {
  it("lists sessions and maps pause/resume/cancel responses", async () => {
    mockTransfers([session({ direction: "pull" })], () => session({ direction: "pull", status: "paused" }));
    mocks.postJson.mockResolvedValue({ transfer: session({ direction: "pull", status: "paused" }) });

    expect(await listTransfers("a1")).toHaveLength(1);
    expect((await getTransfer("a1", 7)).status).toBe("paused");
    expect((await pauseTransfer("a1", 7)).status).toBe("paused");
    expect(mocks.postJson).toHaveBeenCalledWith(paths.agents.transferPause("a1", 7), {});
    expect((await resumeTransfer("a1", 7)).status).toBe("paused");
    expect(mocks.postJson).toHaveBeenCalledWith(paths.agents.transferResume("a1", 7), {});
    expect((await cancelTransfer("a1", 7)).id).toBe(7);
    expect(mocks.del).toHaveBeenCalledWith(paths.agents.transfer("a1", 7));
  });
});

describe("pushLocalFile", () => {
  it("hashes the file, creates a push session and sends 2 MiB chunks in order", async () => {
    const file = testFile(2 * CHUNK + 10);
    mocks.postJson.mockResolvedValue({ transfer: session({ total_size: file.size }) });
    mockTransfers([], () => {
      const offset = Math.min(mocks.putJson.mock.calls.length * CHUNK, file.size);
      return session({ total_size: file.size, committed_offset: offset, status: offset === file.size ? "completed" : "active" });
    });
    const onProgress = vi.fn();
    const onSession = vi.fn();

    await pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file, t, onProgress, onSession });

    expect(mocks.digest).toHaveBeenCalledTimes(1);
    expect(mocks.postJson).toHaveBeenCalledWith(paths.agents.transfers("a1"), {
      direction: "push",
      path: "/tmp/blob.bin",
      total_size: file.size,
      sha256: "0".repeat(64),
      filename: "blob.bin",
    });
    expect(mocks.putJson).toHaveBeenCalledTimes(3);
    expect(mocks.putJson.mock.calls.map(([, body]) => body.offset)).toEqual([0, CHUNK, 2 * CHUNK]);
    expect(mocks.putJson.mock.calls.map(([, body]) => body.final)).toEqual([false, false, true]);
    expect(mocks.putJson.mock.calls[0]?.[2]).toEqual({ signal: undefined });
    expect(mocks.pollTask).toHaveBeenCalledTimes(3);
    expect(mocks.pollTask).toHaveBeenCalledWith(
      "a1", 11, expect.objectContaining({ timeoutMs: TASK_HARD_TIMEOUT_MS }),
    );
    expect(onProgress).toHaveBeenLastCalledWith(100);
    expect(onSession).toHaveBeenCalled();
  });

  it("resumes from the server-confirmed offset after a refresh", async () => {
    const file = testFile(2 * CHUNK + 10);
    mockTransfers(
      [session({ total_size: file.size, committed_offset: CHUNK, status: "paused", sha256: "0".repeat(64) })],
      () => {
        const offset = Math.min(CHUNK + mocks.putJson.mock.calls.length * CHUNK, file.size);
        return session({ total_size: file.size, committed_offset: offset, status: offset === file.size ? "completed" : "active" });
      },
    );
    const onProgress = vi.fn();

    await pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file, t, onProgress });

    expect(mocks.postJson).not.toHaveBeenCalled();
    expect(mocks.putJson.mock.calls.map(([, body]) => body.offset)).toEqual([CHUNK, 2 * CHUNK]);
    expect(onProgress).toHaveBeenNthCalledWith(1, 50);
    expect(onProgress).toHaveBeenLastCalledWith(100);
  });

  it("starts a fresh session when the stored push is failed", async () => {
    mockTransfers(
      [session({ status: "failed", error: "disk full", sha256: "0".repeat(64) })],
      () => session({ total_size: 8, committed_offset: 8, status: "completed" }),
    );
    mocks.postJson.mockResolvedValue({ transfer: session({ total_size: 8 }) });

    await pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file: testFile(8), t });

    expect(mocks.postJson).toHaveBeenCalledTimes(1);
    expect(mocks.putJson).toHaveBeenCalledTimes(1);
    expect(mocks.putJson.mock.calls[0]?.[1]).toMatchObject({ offset: 0, final: true });
  });

  it("rejects files over 50 MiB before any request", async () => {
    const big = testFile(FILE_TRANSFER_MAX_BYTES + 1);
    await expect(
      pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file: big, t }),
    ).rejects.toThrow("agents.files_upload_too_large");
    expect(mocks.postJson).not.toHaveBeenCalled();
    expect(t).toHaveBeenCalledWith("agents.files_upload_too_large", { max: "50 MB" });
  });

  it("sends one empty final chunk for a zero-byte file", async () => {
    const file = testFile(0, "empty.txt");
    mocks.postJson.mockResolvedValue({ transfer: session({ total_size: 0 }) });
    mockTransfers([], () => session({ total_size: 0, committed_offset: 0, status: "completed" }));

    await pushLocalFile({ agentId: "a1", destPath: "/tmp/empty.txt", file, t });

    expect(mocks.putJson).toHaveBeenCalledTimes(1);
    expect(mocks.putJson.mock.calls[0]?.[1]).toMatchObject({ offset: 0, data_base64: "", final: true });
  });

  it("surfaces task failures and aborts", async () => {
    mocks.pollTask.mockResolvedValue({ status: "failed", error: "chunk rejected" });
    mocks.postJson.mockResolvedValue({ transfer: session({ total_size: 8 }) });
    await expect(
      pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file: testFile(8), t }),
    ).rejects.toThrow("chunk rejected");

    const controller = new AbortController();
    controller.abort();
    await expect(
      pushLocalFile({ agentId: "a1", destPath: "/tmp/blob.bin", file: testFile(8), t, signal: controller.signal }),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("pullRemoteFile", () => {
  it("creates a pull transfer, resumes it and exposes committed-byte progress", async () => {
    const detail = session({ direction: "pull", total_size: 2 * CHUNK, committed_offset: 0 });
    mocks.postJson.mockResolvedValue({ transfer: detail, task_id: 12 });
    mocks.putJson.mockResolvedValue({ task_id: 12 });
    mockTransfers([], () => detail);
    let polls = 0;
    mocks.pollTask.mockImplementation(async () => {
      polls += 1;
      detail.committed_offset = polls * CHUNK;
      return { status: "completed" };
    });
    const onProgress = vi.fn();

    const out = await pullRemoteFile({
      agentId: "a1", remotePath: "/tmp/pull.bin", filename: "pull.bin", fileSize: 2 * CHUNK, t, onProgress,
    });

    expect(out).toBe("pulled");
    expect(mocks.postJson).toHaveBeenCalledWith(paths.agents.transfers("a1"), {
      direction: "pull", path: "/tmp/pull.bin", total_size: 2 * CHUNK, filename: "pull.bin",
    });
    expect(mocks.postJson.mock.calls.some(([p]) => p === paths.agents.transferResume("a1", 7))).toBe(true);
    expect(mocks.pollTask).toHaveBeenCalledTimes(2);
    expect(mocks.downloadGet).toHaveBeenCalledWith(paths.agents.transferFile("a1", 7));
    expect(onProgress).toHaveBeenNthCalledWith(1, transferProgressAt(0, {
      total: 2 * CHUNK, chunk: CHUNK, partial: false,
    }));
    expect(onProgress).toHaveBeenLastCalledWith(transferProgressAt(2 * CHUNK, {
      total: 2 * CHUNK, chunk: CHUNK, partial: false,
    }));
  });

  it("resumes an existing session from the API offset after refresh", async () => {
    const stored = session({
      direction: "pull", remote_path: "/tmp/pull.bin", total_size: 3 * CHUNK, committed_offset: CHUNK,
    });
    mockTransfers([stored], () => ({ ...stored, committed_offset: 3 * CHUNK, status: "completed" }));
    const onProgress = vi.fn();

    await pullRemoteFile({
      agentId: "a1", remotePath: "/tmp/pull.bin", filename: "pull.bin", fileSize: 3 * CHUNK, t, onProgress,
    });

    expect(mocks.postJson).not.toHaveBeenCalledWith(paths.agents.transfers("a1"), expect.anything());
    expect(mocks.postJson.mock.calls.some(([p]) => p === paths.agents.transferResume("a1", 7))).toBe(true);
    expect(onProgress).toHaveBeenNthCalledWith(1, transferProgressAt(CHUNK, {
      total: 3 * CHUNK, chunk: CHUNK, partial: false,
    }));
  });

  it("surfaces a failed task and honours an already-aborted signal", async () => {
    mocks.postJson.mockResolvedValue({ transfer: session({ direction: "pull" }), task_id: 12 });
    mockTransfers([], () => session({ direction: "pull" }));
    mocks.pollTask.mockResolvedValue({ status: "failed", error: "beacon offline" });
    await expect(
      pullRemoteFile({ agentId: "a1", remotePath: "/x", filename: "x", fileSize: 8, t }),
    ).rejects.toThrow("beacon offline");

    const controller = new AbortController();
    controller.abort();
    await expect(
      pullRemoteFile({ agentId: "a1", remotePath: "/x", filename: "x", fileSize: 8, t, signal: controller.signal }),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});
