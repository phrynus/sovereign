import { describe, it, expect, vi } from "vitest";
import { mergeSnapshotWithPrev } from "./useAgentDetail";

interface Snap {
  tasks?: { id: number; status?: string; created_at?: string; updated_at?: string; result?: string; error?: string }[];
  tasks_redacted?: boolean;
  total_tasks?: number;
}

describe("mergeSnapshotWithPrev", () => {
  it("returns the snapshot untouched when there is no previous state", () => {
    const snap: Snap = { tasks: [{ id: 1, status: "pending" }] };
    expect(mergeSnapshotWithPrev(snap, null)).toBe(snap);
  });

  it("adopts WS-advanced status/result the snapshot has not caught up to", () => {
    const snap: Snap = { tasks: [{ id: 5, status: "running" }], total_tasks: 1 };
    const prev: Snap = { tasks: [{ id: 5, status: "completed", result: "whoami" }] };

    const next = mergeSnapshotWithPrev(snap, prev);

    expect(next.tasks?.[0]).toMatchObject({ id: 5, status: "completed", result: "whoami" });
    expect(next.total_tasks).toBe(1);
  });

  it("keeps snapshot fields the WS never advanced and ignores unknown ids", () => {
    const snap: Snap = {
      tasks: [
        { id: 5, status: "completed", result: "fresh" },
        { id: 6, status: "pending" },
      ],
    };
    const prev: Snap = {
      tasks: [
        { id: 5, status: "completed", result: "fresh" },
        { id: 999, status: "completed", result: "ghost" },
      ],
    };

    const next = mergeSnapshotWithPrev(snap, prev);

    // Nothing advanced: same reference back, no copies.
    expect(next).toBe(snap);
  });

  it("never overwrites snapshot truth with an older WS fallback", () => {
    const snap: Snap = { tasks: [{ id: 5, status: "completed", result: "FULL" }] };
    const prev: Snap = { tasks: [{ id: 5, status: "completed" }] };

    const next = mergeSnapshotWithPrev(snap, prev);

    expect(next).toBe(snap);
  });

  it("merges per-task: advances one row while leaving siblings alone", () => {
    const snap: Snap = {
      tasks: [
        { id: 1, status: "pending" },
        { id: 2, status: "pending" },
      ],
    };
    const prev: Snap = {
      tasks: [
        { id: 1, status: "failed", error: "timeout" },
        { id: 2, status: "pending" },
      ],
    };

    const next = mergeSnapshotWithPrev(snap, prev);

    expect(next.tasks?.[0]).toMatchObject({ id: 1, status: "failed", error: "timeout" });
    expect(next.tasks?.[1]).toEqual({ id: 2, status: "pending" });
  });

  it("merges only status and timestamps when either snapshot is redacted", () => {
    const readResult = vi.fn(() => "SECRET_RESULT_MARKER");
    const readError = vi.fn(() => "SECRET_ERROR_MARKER");
    const snap: Snap = {
      tasks_redacted: true,
      tasks: [{ id: 5, status: "running", created_at: "2026-08-14T00:00:00Z", updated_at: "2026-08-14T00:01:00Z" }],
      total_tasks: 1,
    };
    const prev: Snap = {
      tasks_redacted: true,
      tasks: [{
        id: 5,
        status: "completed",
        created_at: "2026-08-14T00:00:00Z",
        updated_at: "2026-08-14T00:02:00Z",
        get result() { return readResult(); },
        get error() { return readError(); },
      }],
      total_tasks: 1,
    };

    const next = mergeSnapshotWithPrev(snap, prev);

    expect(next.tasks?.[0]).toEqual({
      id: 5,
      status: "completed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:02:00Z",
    });
    expect(readResult).not.toHaveBeenCalled();
    expect(readError).not.toHaveBeenCalled();
  });
});
