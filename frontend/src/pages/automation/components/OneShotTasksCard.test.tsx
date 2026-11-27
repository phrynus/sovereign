import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, postMock, delMock, fetchAgentListCachedMock, toastErrorMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postMock: vi.fn(),
  delMock: vi.fn(),
  fetchAgentListCachedMock: vi.fn(),
  toastErrorMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    post: (...args: unknown[]) => postMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: (...args: unknown[]) => fetchAgentListCachedMock(...args),
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

import { OneShotTasksCard } from "./OneShotTasksCard";

describe("OneShotTasksCard", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    delMock.mockReset();
    fetchAgentListCachedMock.mockReset();
    toastErrorMock.mockReset();
  });

  it("renders scheduled one-shot tasks", async () => {
    getMock.mockResolvedValue({
      tasks: [
        {
          id: 1,
          agent_id: "agent-alpha-1234",
          type: "shell",
          command: "whoami",
          run_at: "2026-11-27T15:00:00Z",
          status: "pending",
        },
      ],
    });
    fetchAgentListCachedMock.mockResolvedValue([{ id: "agent-alpha-1234", hostname: "WIN-01", status: "online" }]);

    render(<OneShotTasksCard />);

    await waitFor(() => expect(screen.getByText("whoami")).toBeTruthy());
    expect(screen.getByText("pending")).toBeTruthy();
  });

  it("shows empty state when no tasks exist and opens creation form", async () => {
    getMock.mockResolvedValue({ tasks: [] });
    fetchAgentListCachedMock.mockResolvedValue([{ id: "a1", hostname: "HOST-1", status: "online" }]);

    render(<OneShotTasksCard />);

    await waitFor(() => expect(screen.getByText("oneshot.empty_title")).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: /oneshot\.new/i }));
    expect(screen.getByText("oneshot.schedule_btn")).toBeTruthy();
  });

  it("surfaces normalized agent-load error inside the form", async () => {
    getMock.mockResolvedValue({ tasks: [] });
    fetchAgentListCachedMock.mockRejectedValueOnce({ status: 403, message: "denied" });

    render(<OneShotTasksCard />);

    await waitFor(() => expect(screen.getByText("oneshot.empty_title")).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: /oneshot\.new/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
  });
});
