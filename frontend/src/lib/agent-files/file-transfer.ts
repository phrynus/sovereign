import { api, pollTask } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { TASK_HARD_TIMEOUT_MS } from "@/lib/task-poll";
import { downloadBlob } from "@/lib/download";
import {
  FILE_TRANSFER_CHUNK_SIZE,
  FILE_TRANSFER_MAX_BYTES,
  bytesToBase64,
  exfilBasename,
  transferPercent,
  transferProgressAt,
  type TransferProgress,
} from "./file-task";

type TFn = (key: string, params?: Record<string, string | number>) => string;

export type TransferDirection = "push" | "pull";
export type TransferStatus = "active" | "paused" | "completed" | "failed" | "cancelled" | "expired";

/** The server-owned half of a durable transfer. Committed bytes are the only progress source. */
export interface TransferSession {
  id: number;
  agent_id?: string;
  direction: TransferDirection;
  remote_path: string;
  filename?: string;
  total_size: number;
  committed_offset: number;
  chunk_size?: number;
  sha256?: string;
  computed_sha256?: string;
  status: TransferStatus;
  error?: string;
  current_task_id?: number | null;
  created_at?: string;
  updated_at?: string;
  expires_at?: string;
  completed_at?: string | null;
}

export interface CreateTransferRequest {
  direction: TransferDirection;
  path: string;
  total_size: number;
  sha256?: string;
  filename?: string;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  // Validated object boundary: every member read below narrows its own value.
  return value as Record<string, unknown>;
}

function numericField(value: unknown): number | null {
  const n = Number(value);
  return Number.isFinite(n) && n >= 0 ? n : null;
}

function sessionOf(data: unknown): TransferSession | null {
  const outer = asRecord(data);
  if (!outer) return null;
  const raw = asRecord(outer.transfer) ?? outer;
  const id = numericField(raw.id);
  const path = raw.remote_path;
  if (id === null || id <= 0 || typeof path !== "string") return null;
  const direction = raw.direction;
  const status = raw.status;
  return {
    id,
    agent_id: typeof raw.agent_id === "string" ? raw.agent_id : undefined,
    direction: direction === "pull" ? "pull" : "push",
    remote_path: path,
    filename: typeof raw.filename === "string" ? raw.filename : exfilBasename(path),
    total_size: numericField(raw.total_size) ?? 0,
    committed_offset: numericField(raw.committed_offset) ?? 0,
    chunk_size: numericField(raw.chunk_size) ?? FILE_TRANSFER_CHUNK_SIZE,
    computed_sha256: typeof raw.computed_sha256 === "string" ? raw.computed_sha256 : undefined,
    sha256: typeof raw.sha256 === "string" ? raw.sha256 : undefined,
    status: status === "paused" || status === "completed" || status === "failed" ||
      status === "cancelled" || status === "expired" ? status : "active",
    error: typeof raw.error === "string" ? raw.error : undefined,
    current_task_id: numericField(raw.current_task_id) ?? undefined,
    created_at: typeof raw.created_at === "string" ? raw.created_at : undefined,
    updated_at: typeof raw.updated_at === "string" ? raw.updated_at : undefined,
    expires_at: typeof raw.expires_at === "string" ? raw.expires_at : undefined,
    completed_at: typeof raw.completed_at === "string" ? raw.completed_at : undefined,
  };
}

function taskIdOf(data: unknown): number | null {
  const raw = asRecord(data);
  if (!raw) return null;
  const id = numericField(raw.task_id);
  return id !== null && id > 0 ? id : null;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  if (signal?.aborted) {
    reject(new DOMException("aborted", "AbortError"));
    return promise;
  }
  const onAbort = () => {
    clearTimeout(timer);
    reject(new DOMException("aborted", "AbortError"));
  };
  const timer = setTimeout(() => {
    signal?.removeEventListener("abort", onAbort);
    resolve();
  }, ms);
  signal?.addEventListener("abort", onAbort, { once: true });
  return promise;
}

function assertNotAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new DOMException("aborted", "AbortError");
}

function isTerminal(status: TransferStatus): boolean {
  return status === "completed" || status === "failed" || status === "cancelled" || status === "expired";
}

function progressOf(session: TransferSession): TransferProgress {
  const total = session.total_size > 0 ? session.total_size : FILE_TRANSFER_CHUNK_SIZE;
  return transferProgressAt(session.committed_offset, {
    total,
    chunk: FILE_TRANSFER_CHUNK_SIZE,
    partial: session.total_size <= 0 && session.status !== "completed",
  });
}

/** All sessions for one agent, newest first. Survives page reloads and restarts. */
export async function listTransfers(agentId: string): Promise<TransferSession[]> {
  const data = await api.get<{ transfers?: unknown[] }>(paths.agents.transfers(agentId));
  const raw = asRecord(data);
  const rows = raw && Array.isArray(raw.transfers) ? raw.transfers : [];
  return rows.map(sessionOf).filter((s): s is TransferSession => s !== null);
}

export async function getTransfer(agentId: string, transferId: number, signal?: AbortSignal): Promise<TransferSession> {
  const data = await api.get(paths.agents.transfer(agentId, transferId), { signal });
  const session = sessionOf(data);
  if (!session) throw new Error("transfer not found");
  return session;
}

export async function createTransfer(agentId: string, req: CreateTransferRequest): Promise<TransferSession> {
  const data = await api.postJson(paths.agents.transfers(agentId), req);
  const session = sessionOf(data);
  if (!session) throw new Error("transfer could not be created");
  return session;
}

export async function pauseTransfer(agentId: string, transferId: number): Promise<TransferSession> {
  const data = await api.postJson(paths.agents.transferPause(agentId, transferId), {});
  const session = sessionOf(data);
  if (!session) throw new Error("transfer could not be paused");
  return session;
}

/** Queues the next download chunk for a pull. Push resumes by sending the next chunk. */
export async function resumeTransfer(agentId: string, transferId: number): Promise<TransferSession> {
  const data = await api.postJson(paths.agents.transferResume(agentId, transferId), {});
  const session = sessionOf(data);
  if (!session) throw new Error("transfer could not be resumed");
  return session;
}

export async function cancelTransfer(agentId: string, transferId: number): Promise<TransferSession> {
  const data = await api.del(paths.agents.transfer(agentId, transferId));
  const session = sessionOf(data);
  if (!session) throw new Error("transfer could not be cancelled");
  return session;
}

function resumable(
  sessions: TransferSession[],
  remotePath: string,
  direction: TransferDirection,
  expected?: { size?: number; sha256?: string },
): TransferSession | undefined {
  return sessions.find((s) =>
    s.direction === direction &&
    s.remote_path === remotePath &&
    !isTerminal(s.status) &&
    (expected?.size === undefined || s.total_size === expected.size) &&
    (expected?.sha256 === undefined || s.sha256 === expected.sha256));
}

/** Download a completed pull with the server's Range-capable file endpoint. */
export async function downloadTransferFile(
  agentId: string,
  transferId: number,
  fallbackName: string,
  signal?: AbortSignal,
): Promise<void> {
  const { blob, filename } = await api.downloadGet(
    paths.agents.transferFile(agentId, transferId),
    fallbackName,
    { signal },
  );
  downloadBlob(blob, filename || fallbackName);
}

/** The ACK is committed before a task completes; allow a short read-repair window. */
async function waitForCommit(
  agentId: string,
  transferId: number,
  previousOffset: number,
  signal?: AbortSignal,
): Promise<TransferSession> {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    assertNotAborted(signal);
    const session = await getTransfer(agentId, transferId, signal);
    if (session.committed_offset > previousOffset || session.status === "completed") return session;
    if (session.status === "failed" || session.status === "cancelled" || session.status === "expired") {
      throw new Error(session.error || `transfer ${session.status}`);
    }
    if (attempt < 7) await sleep(250, signal);
  }
  throw new Error("transfer confirmation timed out");
}

async function hashFile(file: File): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", await file.arrayBuffer());
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

interface StagedRemoteFile {
  /** Server-side transfer file URL; supports HTTP Range once completed. */
  url: string;
  filename: string;
  partial: boolean;
}

/**
 * Pull a remote file into the durable server-side transfer session without
 * buffering it in the browser. Media playback uses the returned Range URL.
 * A rewritten page finds its session by path through GET /transfers.
 */
export async function stageRemoteFile(opts: {
  agentId: string;
  remotePath: string;
  filename: string;
  fileSize?: number;
  t: TFn;
  transferId?: number;
  fresh?: boolean;
  signal?: AbortSignal;
  onProgress?: (p: TransferProgress) => void;
}): Promise<StagedRemoteFile> {
  assertNotAborted(opts.signal);
  const size = opts.fileSize ?? 0;
  if (size > FILE_TRANSFER_MAX_BYTES) {
    throw new Error(opts.t("agents.files_upload_too_large", { max: "50 MB" }));
  }
  let session: TransferSession | null = null;
  if (!opts.fresh) {
    const sessions = opts.transferId
      ? [await getTransfer(opts.agentId, opts.transferId, opts.signal)]
      : await listTransfers(opts.agentId);
    session = opts.transferId
      ? sessions[0] ?? null
      : resumable(sessions, opts.remotePath, "pull", size > 0 ? { size } : undefined) ?? null;
    if (session && (session.direction !== "pull" || isTerminal(session.status))) session = null;
  }
  if (!session) {
    session = await createTransfer(opts.agentId, {
      direction: "pull",
      path: opts.remotePath,
      total_size: size,
      filename: opts.filename,
    });
    // A concurrent operator may have created the same path in the meantime.
    if (session.direction !== "pull") throw new Error(opts.t("agents.files_pull_failed"));
  }
  opts.onProgress?.(progressOf(session));
  while (!isTerminal(session.status) && (session.total_size <= 0 || session.committed_offset < session.total_size)) {
    assertNotAborted(opts.signal);
    const before = session.committed_offset;
    const queued = await api.postJson(paths.agents.transferResume(opts.agentId, session.id), {}, { signal: opts.signal });
    const taskId = taskIdOf(queued) ?? session.current_task_id;
    if (!taskId) throw new Error(opts.t("agents.files_pull_failed"));
    const st = await pollTask(opts.agentId, taskId, { timeoutMs: TASK_HARD_TIMEOUT_MS, signal: opts.signal });
    if (st.status === "failed") throw new Error(st.error || opts.t("agents.files_pull_failed"));
    session = await waitForCommit(opts.agentId, session.id, before, opts.signal);
    if (session.committed_offset > FILE_TRANSFER_MAX_BYTES) {
      throw new Error(opts.t("agents.files_upload_too_large", { max: "50 MB" }));
    }
    opts.onProgress?.(progressOf(session));
  }
  if (session.status === "failed" || session.status === "cancelled" || session.status === "expired") {
    throw new Error(session.error || `transfer ${session.status}`);
  }
  return {
    url: paths.agents.transferFile(opts.agentId, session.id),
    filename: session.filename || opts.filename,
    partial: session.status !== "completed",
  };
}

export async function pullRemoteFile(opts: {
  agentId: string;
  remotePath: string;
  filename: string;
  fileSize?: number;
  t: TFn;
  transferId?: number;
  fresh?: boolean;
  signal?: AbortSignal;
  onProgress?: (p: TransferProgress) => void;
}): Promise<"pulled" | "partial"> {
  const staged = await stageRemoteFile(opts);
  const { blob, filename: saved } = await api.downloadGet(staged.url);
  downloadBlob(blob, saved || opts.filename);
  return staged.partial ? "partial" : "pulled";
}

/**
 * Push a local file in sequential 2 MiB chunks. Every chunk is acknowledged by
 * the server (committed_offset) before the next one is sent, so a refresh can
 * continue from the durable session instead of restarting the file.
 */
export async function pushLocalFile(opts: {
  agentId: string;
  destPath: string;
  file: File;
  t: TFn;
  onProgress?: (pct: number) => void;
  onSession?: (session: TransferSession) => void;
  transferId?: number;
  fresh?: boolean;
  signal?: AbortSignal;
}): Promise<void> {
  if (opts.file.size > FILE_TRANSFER_MAX_BYTES) {
    throw new Error(opts.t("agents.files_upload_too_large", { max: "50 MB" }));
  }
  assertNotAborted(opts.signal);
  const digest = await hashFile(opts.file);
  let session: TransferSession | null = null;
  if (!opts.fresh) {
    const sessions = opts.transferId
      ? [await getTransfer(opts.agentId, opts.transferId, opts.signal)]
      : await listTransfers(opts.agentId);
    session = opts.transferId
      ? sessions[0] ?? null
      : resumable(sessions, opts.destPath, "push", { size: opts.file.size, sha256: digest }) ?? null;
    if (session && (session.direction !== "push" || isTerminal(session.status))) session = null;
  }
  if (!session) {
    session = await createTransfer(opts.agentId, {
      direction: "push",
      path: opts.destPath,
      total_size: opts.file.size,
      sha256: digest,
      filename: opts.file.name,
    });
    if (session.direction !== "push") throw new Error(opts.t("agents.files_push_failed"));
  }
  if (isTerminal(session.status)) throw new Error(session.error || `transfer ${session.status}`);
  opts.onSession?.(session);
  let offset = session.committed_offset;
  opts.onProgress?.(transferPercent({ offset, total: opts.file.size }));
  let sent = false;
  while (offset < opts.file.size || !sent) {
    assertNotAborted(opts.signal);
    const size = Math.min(FILE_TRANSFER_CHUNK_SIZE, opts.file.size - offset);
    const bytes = new Uint8Array(await opts.file.slice(offset, offset + size).arrayBuffer());
    const final = offset + size >= opts.file.size;
    const queued = await api.putJson(
      paths.agents.transferChunk(opts.agentId, session.id),
      { offset, data_base64: bytesToBase64(bytes), final },
      { signal: opts.signal },
    );
    const taskId = taskIdOf(queued) ?? session.current_task_id;
    if (!taskId) throw new Error(opts.t("agents.files_push_failed"));
    const st = await pollTask(opts.agentId, taskId, { timeoutMs: TASK_HARD_TIMEOUT_MS, signal: opts.signal });
    if (st.status === "failed") throw new Error(st.error || opts.t("agents.files_push_failed"));
    session = await waitForCommit(opts.agentId, session.id, offset, opts.signal);
    offset = session.committed_offset;
    sent = true;
    opts.onProgress?.(transferPercent({ offset, total: opts.file.size }));
    opts.onSession?.(session);
  }
}
