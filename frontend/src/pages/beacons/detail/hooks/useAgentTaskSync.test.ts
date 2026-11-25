import { describe, it, expect, vi } from "vitest";
import { applyTaskUpdate, frameAction } from "./useAgentTaskSync";
import type { AgentDetailResponse, TaskEntry } from "../components/agent-detail-utils";

function snapshot(tasks: TaskEntry[] = [], stats: Partial<AgentDetailResponse> = {}): AgentDetailResponse {
  return { agent: {}, tasks, total_tasks: tasks.length, pending_tasks: 0, completed_tasks: 0, failed_tasks: 0, ...stats };
}

describe("frameAction", () => {
  it("treats the reconnect sync frame as a resync, not as an unrelated agent", () => {
    // The server sends {"type":"sync"} on every connect, with no agent_id.
    // Ignoring it (the previous behaviour) meant a client that had been
    // disconnected for a terminal task event kept its spinner forever.
    expect(frameAction({ type: "sync" }, "a1")).toBe("resync");
  });

  it("still ignores another agent's events", () => {
    expect(frameAction({ type: "task_update", agent_id: "a2" }, "a1")).toBe("ignore");
    expect(frameAction({ type: "agent_offline", agent_id: "a2" }, "a1")).toBe("ignore");
  });

  it("classifies this agent's frames", () => {
    expect(frameAction({ type: "task_update", agent_id: "a1" }, "a1")).toBe("task");
    expect(frameAction({ type: "agent_online", agent_id: "a1" }, "a1")).toBe("reload");
    expect(frameAction({ type: "agent_offline", agent_id: "a1" }, "a1")).toBe("reload");
    expect(frameAction({ type: "agent_data_update", agent_id: "a1" }, "a1")).toBe("patch");
    expect(frameAction({ type: "screenshot_frame", agent_id: "a1" }, "a1")).toBe("ignore");
  });
});

describe("applyTaskUpdate", () => {
  it("inserts a brand-new task at the head with stats bump", () => {
    const prev = snapshot([
      { id: 10, type: "shell", status: "completed", result: "old" },
    ], { total_tasks: 2, completed_tasks: 1 });
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      agent_id: "a1",
      task_id: 11,
      task_type: "screenshot",
      status: "pending",
      command: "",
    });
    expect(next.tasks?.length).toBe(2);
    expect(next.tasks?.[0]).toMatchObject({ id: 11, type: "screenshot", status: "pending" });
    expect(next.tasks?.[1]?.id).toBe(10);
    expect(next.total_tasks).toBe(3);
    expect(next.pending_tasks).toBe(1);
    expect(next.completed_tasks).toBe(1);
  });

  it("flips existing pending -> completed and adjusts stats once", () => {
    const prev = snapshot([
      { id: 5, type: "shell", status: "pending" },
    ], { total_tasks: 3, pending_tasks: 1, completed_tasks: 1, failed_tasks: 1 });
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      agent_id: "a1",
      task_id: 5,
      task_type: "shell",
      status: "completed",
      result: "whoami",
    });
    expect(next.tasks?.[0]?.status).toBe("completed");
    expect(next.pending_tasks).toBe(0);
    expect(next.completed_tasks).toBe(2);
    expect(next.failed_tasks).toBe(1);
    expect(next.total_tasks).toBe(3);
  });

  it("never replaces an existing full result with the truncated WS preview", () => {
    const prev = snapshot([
      { id: 7, type: "shell", status: "completed", result: "FULL-RESULT-BODY" },
    ], { completed_tasks: 1 });
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      agent_id: "a1",
      task_id: 7,
      status: "completed",
      result: "preview…",
    });
    expect(next.tasks?.[0]?.result).toBe("FULL-RESULT-BODY");
  });

  it("fills an empty result with the preview", () => {
    const prev = snapshot([{ id: 7, type: "shell", status: "completed" }], { completed_tasks: 1 });
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      agent_id: "a1",
      task_id: 7,
      status: "completed",
      result: "preview…",
    });
    expect(next.tasks?.[0]?.result).toBe("preview…");
  });

  it("ignores frames with invalid task ids", () => {
    const prev = snapshot([{ id: 1, type: "shell", status: "completed" }], { completed_tasks: 1 });
    const next = applyTaskUpdate(prev, { type: "task_update", agent_id: "a1", task_id: 0, status: "completed" });
    expect(next).toBe(prev);
  });

  it("no-ops on same-status frames", () => {
    const prev = snapshot([{ id: 3, type: "shell", status: "completed" }], { completed_tasks: 1 });
    const next = applyTaskUpdate(prev, { type: "task_update", agent_id: "a1", task_id: 3, status: "completed" });
    expect(next).toBe(prev);
  });

  it("adjusts failed -> pending", () => {
    const prev = snapshot([{ id: 3, type: "shell", status: "failed" }], { total_tasks: 2, failed_tasks: 1, pending_tasks: 0 });
    const next = applyTaskUpdate(prev, { type: "task_update", agent_id: "a1", task_id: 3, status: "pending" });
    expect(next.tasks?.[0]?.status).toBe("pending");
    expect(next.failed_tasks).toBe(0);
    expect(next.pending_tasks).toBe(1);
  });

  it("does not re-prepend a task already accounted for (seen set)", () => {
    const prev = snapshot([
      { id: 11, type: "screenshot", status: "pending" },
    ], { total_tasks: 3, pending_tasks: 1 });
    const seen = new Set([11]);
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      agent_id: "a1",
      task_id: 11,
      task_type: "screenshot",
      status: "pending",
      command: "",
    }, seen);
    expect(next).toBe(prev);
    expect(next.tasks?.length).toBe(1);
    expect(next.total_tasks).toBe(3);
    expect(next.pending_tasks).toBe(1);
  });

  it("updates only status and timestamps for a redacted task", () => {
    const readType = vi.fn(() => "SECRET_TYPE_MARKER");
    const readCommand = vi.fn(() => "SECRET_COMMAND_MARKER");
    const readResult = vi.fn(() => "SECRET_RESULT_MARKER");
    const readError = vi.fn(() => "SECRET_ERROR_MARKER");
    const readCreator = vi.fn(() => "SECRET_CREATOR_MARKER");
    const prev = snapshot([{
      id: 1,
      status: "running",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:01:00Z",
    }], { tasks_redacted: true, total_tasks: 1, pending_tasks: 0, completed_tasks: 0, failed_tasks: 0 });

    const next = applyTaskUpdate(prev, {
      type: "task_update",
      task_id: 1,
      status: "completed",
      updated_at: "2026-08-14T00:02:00Z",
      get task_type() { return readType(); },
      get command() { return readCommand(); },
      get result() { return readResult(); },
      get error() { return readError(); },
      get created_by() { return readCreator(); },
    });

    expect(next.tasks?.[0]).toEqual({
      id: 1,
      status: "completed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:02:00Z",
    });
    expect(next.total_tasks).toBe(1);
    expect(next.completed_tasks).toBe(0);
    expect(readType).not.toHaveBeenCalled();
    expect(readCommand).not.toHaveBeenCalled();
    expect(readResult).not.toHaveBeenCalled();
    expect(readError).not.toHaveBeenCalled();
    expect(readCreator).not.toHaveBeenCalled();
  });

  it("inserts only a summary for a new redacted task without deriving counters", () => {
    const prev = snapshot([], { tasks_redacted: true, total_tasks: 4, pending_tasks: 0, completed_tasks: 0, failed_tasks: 0 });
    const next = applyTaskUpdate(prev, {
      type: "task_update",
      task_id: 2,
      status: "pending",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:00:00Z",
    }, new Set());

    expect(next.tasks?.[0]).toEqual({
      id: 2,
      status: "pending",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:00:00Z",
    });
    expect(next.total_tasks).toBe(4);
    expect(next.pending_tasks).toBe(0);
  });
});