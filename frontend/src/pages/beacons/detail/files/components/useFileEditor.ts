import { useCallback, useEffect, useRef, useState } from "react";
import { api, pollTask, type TaskStatus } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { TASK_HARD_TIMEOUT_MS } from "@/lib/task-poll";
import { errorText } from "@/lib/errors";
import {
  EDITOR_MAX_BYTES,
  base64ToUtf8,
  fileTaskId,
  isFileConflictError,
  utf8ToBase64,
} from "@/lib/agent-files/file-task";

type TFn = (key: string, params?: Record<string, string | number>) => string;

/** Task detail carries editor hash/mtime fields absent from the narrow TaskStatus type. */
type EditorStatus = TaskStatus & { result_sha256?: string; result_mtime?: number };

function editorStatus(status: TaskStatus): EditorStatus {
  return status as EditorStatus;
}

export interface FileRevision {
  task_id: number;
  type: string;
  operator: string;
  created_at: string;
  sha256: string;
  mtime: number;
  size: number;
}

interface Baseline {
  taskId: number;
  hash: string;
  mtime: number;
}

/**
 * Optimistic text editing: a completed read (or previous write) provides the
 * hash used by the agent's compare-and-swap. Drafts live only in the browser;
 * public task projections never carry their bytes.
 */
export function useFileEditor(opts: { agentId: string; path: string; t: TFn }) {
  const { agentId, path, t } = opts;
  const [draft, setDraft] = useState("");
  const [original, setOriginal] = useState("");
  const [baseline, setBaseline] = useState<Baseline>({ taskId: 0, hash: "", mtime: 0 });
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [revisions, setRevisions] = useState<FileRevision[]>([]);
  const [revisionText, setRevisionText] = useState<string | null>(null);
  const baselineRef = useRef<Baseline>(baseline);
  const draftRef = useRef("");
  const originalRef = useRef("");
  const abortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
      abortRef.current = null;
    };
  }, []);

  const dirty = draft !== original;

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = t("agents.files_editor_dirty_leave");
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty, t]);

  const changeDraft = useCallback((value: string) => {
    draftRef.current = value;
    setDraft(value);
  }, []);

  const applyBaseline = useCallback((next: Baseline, content?: string) => {
    baselineRef.current = next;
    setBaseline(next);
    setConflict(false);
    if (content !== undefined) {
      originalRef.current = content;
      draftRef.current = content;
      setOriginal(content);
      setDraft(content);
    }
  }, []);

  const loadHistory = useCallback(async () => {
    const data = await api.get<{ revisions?: FileRevision[] }>(paths.agents.filesHistory(agentId, path, 50));
    const rows = Array.isArray(data.revisions) ? data.revisions : [];
    if (mountedRef.current) setRevisions(rows);
    return rows;
  }, [agentId, path]);

  /** Fetch a fresh read baseline; keepDraft=true is the Overwrite path. */
  const open = useCallback(async (options?: { keepDraft?: boolean }) => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setLoading(true);
    setError(null);
    try {
      const ack = await api.post(paths.agents.filesRead(agentId), { path }, { signal: ac.signal });
      const taskId = fileTaskId(ack);
      if (!taskId) throw new Error(t("agents.files_read_waiting"));
      const status = await pollTask(agentId, taskId, { signal: ac.signal, timeoutMs: TASK_HARD_TIMEOUT_MS });
      if (status.status === "failed") throw new Error(status.error || t("agents.files_read_failed"));
      const editor = editorStatus(status);
      let hash = editor.result_sha256 ?? "";
      const mtime = editor.result_mtime ?? 0;
      if (!hash) {
        const rows = await loadHistory();
        hash = rows.find((r) => r.task_id === taskId && r.sha256)?.sha256 ?? "";
      }
      if (!hash) throw new Error(t("agents.files_editor_baseline_missing"));
      if (ac.signal.aborted) return null;
      const content = status.status === "completed" ? status.result ?? "" : "";
      if (new TextEncoder().encode(content).length > EDITOR_MAX_BYTES) {
        throw new Error(t("agents.files_preview_too_large"));
      }
      applyBaseline({ taskId, hash, mtime }, options?.keepDraft ? undefined : content);
      if (!options?.keepDraft) setRevisionText(null);
      return { taskId, hash, mtime };
    } catch (err) {
      if (ac.signal.aborted) throw err;
      if (mountedRef.current) setError(errorText(t, err, () => t("agents.files_read_failed")));
      throw err;
    } finally {
      if (mountedRef.current && !ac.signal.aborted) setLoading(false);
    }
  }, [agentId, applyBaseline, loadHistory, path, t]);

  const save = useCallback(async () => {
    const base = baselineRef.current;
    if (!base.taskId || !base.hash) throw new Error(t("agents.files_editor_baseline_missing"));
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setSaving(true);
    setError(null);
    setConflict(false);
    try {
      const content = utf8ToBase64(draftRef.current);
      const ack = await api.post(paths.agents.filesWrite(agentId), {
        path,
        content,
        encoding: "base64",
        base_task_id: String(base.taskId),
        empty: draftRef.current === "" ? "true" : "",
      }, { signal: ac.signal });
      const taskId = fileTaskId(ack);
      if (!taskId) throw new Error(t("agents.files_editor_save_failed"));
      const status = await pollTask(agentId, taskId, { signal: ac.signal, timeoutMs: TASK_HARD_TIMEOUT_MS });
      if (status.status === "failed") {
        throw new Error(status.error || t("agents.files_editor_save_failed"));
      }
      const editor = editorStatus(status);
      let hash = editor.result_sha256 ?? "";
      if (!hash) {
        const rows = await loadHistory();
        hash = rows.find((r) => r.task_id === taskId && r.sha256)?.sha256 ?? "";
      }
      if (ac.signal.aborted) return null;
      const next: Baseline = {
        taskId,
        hash: hash || base.hash,
        mtime: editor.result_mtime || base.mtime,
      };
      applyBaseline(next);
      originalRef.current = draftRef.current;
      setOriginal(draftRef.current);
      void loadHistory().catch(() => { /* history is advisory */ });
      return next;
    } catch (err) {
      if (ac.signal.aborted) throw err;
      if (isFileConflictError(err)) {
        if (mountedRef.current) setConflict(true);
      } else if (mountedRef.current) {
        setError(errorText(t, err, () => t("agents.files_editor_save_failed")));
      }
      throw err;
    } finally {
      if (mountedRef.current && !ac.signal.aborted) setSaving(false);
    }
  }, [agentId, applyBaseline, loadHistory, path, t]);

  /** Discard the local draft and re-read the current remote file. */
  const reload = useCallback(async () => {
    await open();
  }, [open]);

  /** Rebase on a fresh hash before saving the retained draft. */
  const overwrite = useCallback(async () => {
    await open({ keepDraft: true });
    await save();
  }, [open, save]);

  const viewRevision = useCallback(async (taskId: number) => {
    const data = await api.get<{ content_base64?: string }>(paths.agents.filesRevision(agentId, taskId));
    if (!mountedRef.current) return;
    setRevisionText(base64ToUtf8(data.content_base64 ?? ""));
  }, [agentId]);

  return {
    draft, dirty, loading, saving, conflict, error,
    baselineTaskId: baseline.taskId,
    baselineHash: baseline.hash,
    mtime: baseline.mtime,
    revisions, revisionText,
    changeDraft, open, save, reload, overwrite, loadHistory, viewRevision,
    clearRevision: () => setRevisionText(null),
  };
}
