import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, postJsonMock, putJsonMock, delMock, fetchAgentListCachedMock, fetchTaskTypesMock, toastErrorMock, tMock } =
  vi.hoisted(() => ({
    getMock: vi.fn(),
    postJsonMock: vi.fn(),
    putJsonMock: vi.fn(),
    delMock: vi.fn(),
    fetchAgentListCachedMock: vi.fn(),
    fetchTaskTypesMock: vi.fn(),
    toastErrorMock: vi.fn(),
    tMock: (key: string) => key,
  }));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    postJson: (...args: unknown[]) => postJsonMock(...args),
    putJson: (...args: unknown[]) => putJsonMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: (...args: unknown[]) => fetchAgentListCachedMock(...args),
}));

vi.mock("@/lib/taskTypes", () => ({
  fetchTaskTypes: (...args: unknown[]) => fetchTaskTypesMock(...args),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: tMock }),
  getLocale: () => "en",
}));

vi.mock("@/lib/hooks/useErrorToast", () => ({
  useErrorToast: () => ({ toastError: toastErrorMock, warnError: vi.fn() }),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { ScheduledRulesCard } from "./ScheduledRulesCard";

describe("ScheduledRulesCard", () => {
  beforeEach(() => {
    getMock.mockReset();
    postJsonMock.mockReset();
    putJsonMock.mockReset();
    delMock.mockReset();
    fetchAgentListCachedMock.mockReset();
    fetchTaskTypesMock.mockReset();
    toastErrorMock.mockReset();
  });

  it("renders schedule-type automation rules and filters out non-schedule rules", async () => {
    getMock.mockResolvedValue([
      {
        id: "r1",
        name: "Nightly Inventory",
        event_type: "schedule",
        enabled: true,
        agent_id: "agent-1",
        task_type: "hostinfo",
        command: "",
        params: "",
        schedule: "0 2 * * *",
        last_run: "",
        next_run: "",
        run_count: 3,
        created_by: "admin",
        created_at: "2026-11-27T00:00:00Z",
      },
      {
        id: "r2",
        name: "On Checkin",
        event_type: "agent_online",
        enabled: true,
        agent_id: "agent-1",
        task_type: "hostinfo",
        command: "",
        params: "",
        schedule: "",
        last_run: "",
        next_run: "",
        run_count: 0,
        created_by: "admin",
        created_at: "2026-11-27T00:00:00Z",
      },
    ]);
    fetchAgentListCachedMock.mockResolvedValue([{ id: "agent-1", hostname: "SRV-01" }]);
    fetchTaskTypesMock.mockResolvedValue([{ type: "shell", label: "Shell", category: "execution" }]);

    render(<ScheduledRulesCard />);

    await waitFor(() => expect(screen.getByText("Nightly Inventory")).toBeTruthy());
    expect(screen.queryByText("On Checkin")).toBeNull();
  });

  it("validates required fields before saving a new schedule", async () => {
    getMock.mockResolvedValue([]);
    fetchAgentListCachedMock.mockResolvedValue([]);
    fetchTaskTypesMock.mockResolvedValue([{ type: "shell", label: "Shell", category: "execution" }]);

    render(<ScheduledRulesCard />);

    await waitFor(() => expect(screen.getByRole("button", { name: /scheduler\.new_schedule/i })).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: /scheduler\.new_schedule/i }));
    fireEvent.click(screen.getByRole("button", { name: /autotag\.create/i }));

    await waitFor(() => expect(screen.getByText("scheduler.err_name_required")).toBeTruthy());
    expect(postJsonMock).not.toHaveBeenCalled();
  });
});
