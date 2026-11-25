import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { formatTime } from "@/lib/utils";
import type { DashboardRecentTask, DashboardStats } from "@/types/agent";
import { DashboardRecentTasks } from "./AnalyticsView";

vi.mock("@/lib/i18n", () => ({ getLocale: () => "en", useI18n: () => ({ t: (key: string) => key }) }));

function stats(tasks: DashboardRecentTask[], redacted = false): DashboardStats {
  return {
    total_tasks: 42,
    recent_tasks: tasks,
    tasks_redacted: redacted,
  } as DashboardStats;
}

describe("DashboardRecentTasks", () => {
  it("keeps task type and command for an authorized projection", () => {
    render(<DashboardRecentTasks stats={stats([{
      id: 3,
      type: "shell",
      command: "whoami",
      status: "completed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:01:00Z",
    }])} />);

    expect(screen.getByText("shell")).toBeTruthy();
    expect(screen.getByText("whoami")).toBeTruthy();
    expect(screen.getByText(formatTime("2026-08-14T00:00:00Z"))).toBeTruthy();
  });

  it("does not read or render redacted task contents and preserves the explicit total", () => {
    const readType = vi.fn(() => "SECRET_TYPE_MARKER");
    const readCommand = vi.fn(() => "SECRET_COMMAND_MARKER");
    const task: DashboardRecentTask = {
      id: 9,
      status: "failed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:02:00Z",
      get type() { return readType(); },
      get command() { return readCommand(); },
    };
    const { container } = render(<DashboardRecentTasks stats={stats([task], true)} />);

    fireEvent.click(screen.getByText("#9"));

    expect(screen.getByText("42")).toBeTruthy();
    expect(screen.getByText("tasks.failed")).toBeTruthy();
    expect(screen.getByText(formatTime(task.created_at))).toBeTruthy();
    expect(screen.getByText(formatTime(task.updated_at))).toBeTruthy();
    expect(container.textContent).not.toContain("SECRET_");
    expect(container.querySelector("pre")).toBeNull();
    expect(readType).not.toHaveBeenCalled();
    expect(readCommand).not.toHaveBeenCalled();
  });
});
