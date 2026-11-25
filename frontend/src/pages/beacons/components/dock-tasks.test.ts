import { describe, expect, it, vi } from "vitest";
import type { AgentTaskRecord } from "@/types/agent";
import { applyTaskEvent, canApproveOwnTask, canCancelTask, canReviewTask, isDockTaskEvent, shouldRevealTaskResult, taskEventId } from "./dock-tasks";

const row = (over: Partial<AgentTaskRecord> = {}): AgentTaskRecord => ({
  id: 1,
  type: "shell",
  command: "whoami",
  status: "pending",
  created_at: "2026-08-14T00:00:00Z",
  ...over,
});

describe("isDockTaskEvent", () => {
  it("matches this agent's task bus messages only", () => {
    expect(isDockTaskEvent({ type: "task_update", agent_id: "a1" }, "a1")).toBe(true);
    expect(isDockTaskEvent({ type: "task_created", agent_id: "a1" }, "a1")).toBe(true);
    expect(isDockTaskEvent({ type: "task_output", agent_id: "a1" }, "a1")).toBe(true);
    expect(isDockTaskEvent({ type: "task_update", agent_id: "a2" }, "a1")).toBe(false);
    expect(isDockTaskEvent({ type: "agent_online", agent_id: "a1" }, "a1")).toBe(false);
    expect(isDockTaskEvent({ type: "task_update", agent_id: "a1" }, "")).toBe(false);
  });
});

describe("shouldRevealTaskResult", () => {
  it("opens the result only when the task finished", () => {
    expect(shouldRevealTaskResult({ type: "task_update", status: "completed" })).toBe(true);
    expect(shouldRevealTaskResult({ type: "task_update", status: "failed" })).toBe(true);
    expect(shouldRevealTaskResult({ type: "task_update", status: "pending" })).toBe(false);
    expect(shouldRevealTaskResult({ type: "task_output", done: true })).toBe(true);
    expect(shouldRevealTaskResult({ type: "task_output", done: false })).toBe(false);
  });
});

describe("applyTaskEvent", () => {
  it("inserts and patches by task id", () => {
    const inserted = applyTaskEvent([], { type: "task_update", task_id: 9, task_type: "ps", status: "pending", command: "ps" });
    expect(inserted[0]).toMatchObject({ id: 9, type: "ps", status: "pending" });
    const patched = applyTaskEvent(inserted, { type: "task_update", task_id: 9, status: "completed", result: "ok", created_by: "op" });
    expect(patched[0]).toMatchObject({ status: "completed", result: "ok", command: "ps", created_by: "op" });
    expect(canCancelTask("pending")).toBe(true);
    expect(canCancelTask("running")).toBe(true);
    expect(canCancelTask("completed")).toBe(false);
    expect(canCancelTask("pending_approval")).toBe(false);
    expect(canReviewTask("pending_approval")).toBe(true);
    expect(canReviewTask("pending")).toBe(false);
  });

  it("appends streamed output chunks", () => {
    const first = applyTaskEvent([row({ created_by: "alice" })], { type: "task_output", task_id: 1, chunk: "ad", done: false });
    const second = applyTaskEvent(first, { type: "task_output", task_id: 1, chunk: "min", done: true });
    expect(second[0].result).toBe("admin");
    expect(second[0].status).toBe("completed");
    expect(second[0].created_by).toBe("alice");
  });

  it("keeps pending_approval instead of collapsing it to pending", () => {
    const rows = applyTaskEvent([], {
      type: "task_created",
      task_id: 4,
      status: "pending_approval",
      created_by: "alice",
    });
    expect(rows[0].status).toBe("pending_approval");
    expect(canApproveOwnTask("alice", "alice")).toBe(false);
    expect(canApproveOwnTask("alice", "bob")).toBe(true);
    expect(canApproveOwnTask("ai", "alice")).toBe(true);
  });

  it("ignores events without a task id", () => {
    const start = [row()];
    expect(applyTaskEvent(start, { type: "task_update" })).toBe(start);
    expect(taskEventId({ task_id: 4 })).toBe(4);
    expect(taskEventId({})).toBeNull();
  });

  it("reduces redacted events to status and timestamps without reading contents", () => {
    const readType = vi.fn(() => "SECRET_TYPE_MARKER");
    const readCommand = vi.fn(() => "SECRET_COMMAND_MARKER");
    const readResult = vi.fn(() => "SECRET_RESULT_MARKER");
    const readCreator = vi.fn(() => "SECRET_CREATOR_MARKER");
    const next = applyTaskEvent([row()], {
      type: "task_update",
      task_id: 1,
      status: "completed",
      updated_at: "2026-08-14T00:02:00Z",
      get task_type() { return readType(); },
      get command() { return readCommand(); },
      get result() { return readResult(); },
      get created_by() { return readCreator(); },
    }, true);

    expect(next[0]).toEqual({
      id: 1,
      status: "completed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:02:00Z",
    });
    expect(readType).not.toHaveBeenCalled();
    expect(readCommand).not.toHaveBeenCalled();
    expect(readResult).not.toHaveBeenCalled();
    expect(readCreator).not.toHaveBeenCalled();
  });

  it("uses output completion only as a status signal in redacted mode", () => {
    const readChunk = vi.fn(() => "SECRET_OUTPUT_MARKER");
    const next = applyTaskEvent([row({ status: "running" })], {
      type: "task_output",
      task_id: 1,
      done: true,
      get chunk() { return readChunk(); },
    }, true);

    expect(next[0].status).toBe("completed");
    expect(next[0].result).toBeUndefined();
    expect(readChunk).not.toHaveBeenCalled();
  });
});
