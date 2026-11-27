import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, putMock, delMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  putMock: vi.fn(),
  delMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    put: (...args: unknown[]) => putMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: tMock }),
  getLocale: () => "en",
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import NotificationsPage from "./NotificationsPageContent";

describe("NotificationsPageContent", () => {
  beforeEach(() => {
    getMock.mockReset();
    putMock.mockReset();
    delMock.mockReset();
  });

  it("renders notification rows and supports mark-all-read", async () => {
    getMock.mockResolvedValue({
      notifications: [
        {
          id: 42,
          type: "task_completed",
          title: "Task finished",
          message: "hostinfo completed on WIN-01",
          agent_id: "a1",
          task_id: 99,
          severity: "info",
          read: false,
          created_at: "2026-11-27T10:00:00Z",
        },
      ],
      total: 1,
    });
    putMock.mockResolvedValue({});

    render(<NotificationsPage />);

    await waitFor(() => expect(screen.getByText("Task finished")).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: /notifications\.mark_all_read/i }));
    await waitFor(() => expect(putMock).toHaveBeenCalled());
  });

  it("renders empty state when there are no notifications", async () => {
    getMock.mockResolvedValue({ notifications: [], total: 0 });

    render(<NotificationsPage embedded />);

    await waitFor(() => expect(screen.getByText("notifications.empty")).toBeTruthy());
  });
});
