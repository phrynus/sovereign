import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, fetchAgentListCachedMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  fetchAgentListCachedMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
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

vi.mock("@/lib/wsContext", () => ({
  useWS: () => ({ connected: true, subscribe: () => () => {} }),
}));

vi.mock("./components/EffectivenessCard", () => ({
  default: () => <div data-testid="effectiveness-card" />,
}));

vi.mock("./components/BuildCacheCard", () => ({
  default: () => <div data-testid="build-cache-card" />,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import BuildsPage from "./BuildsPageContent";

describe("BuildsPageContent", () => {
  beforeEach(() => {
    getMock.mockReset();
    fetchAgentListCachedMock.mockReset();
  });

  it("renders build logs and deduplicates repeated build ids", async () => {
    getMock.mockResolvedValue({
      logs: [
        {
          id: "b-1",
          platform: "linux",
          format: "elf",
          filename: "agent-linux.elf",
          status: "success",
          user: "admin",
          created_at: "2026-11-27T10:00:00Z",
          started_at: "2026-11-27T10:00:00Z",
          completed_at: "2026-11-27T10:00:12Z",
        },
        {
          id: "b-1",
          platform: "linux",
          format: "elf",
          filename: "agent-linux.elf",
          status: "success",
          user: "admin",
          created_at: "2026-11-27T10:00:00Z",
        },
      ],
      total: 1,
      success_count: 1,
      failed_count: 0,
    });
    fetchAgentListCachedMock.mockResolvedValue([]);

    render(
      <MemoryRouter>
        <BuildsPage />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getAllByText("agent-linux.elf")).toHaveLength(1));
  });

  it("renders empty state when build list is empty", async () => {
    getMock.mockResolvedValue({ logs: [], total: 0 });
    fetchAgentListCachedMock.mockResolvedValue([]);

    render(
      <MemoryRouter>
        <BuildsPage embedded />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText("builds.empty")).toBeTruthy());
  });
});
