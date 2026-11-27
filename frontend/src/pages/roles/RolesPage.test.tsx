import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, postJsonMock, delMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postJsonMock: vi.fn(),
  delMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    postJson: (...args: unknown[]) => postJsonMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: tMock }),
  registerFragment: vi.fn(),
  getLocale: () => "en",
}));

vi.mock("@/components/ui/permission", () => ({
  Permission: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import RolesPage from "./RolesPage";

describe("RolesPage", () => {
  beforeEach(() => {
    getMock.mockReset();
    postJsonMock.mockReset();
    delMock.mockReset();
  });

  it("renders custom roles and their permissions", async () => {
    getMock.mockResolvedValue({
      success: true,
      data: [
        {
          id: 1,
          name: "Auditor",
          description: "Read-only compliance reviewer",
          permissions: ["audit.read", "agents.read"],
          created_at: "2026-11-27T00:00:00Z",
          updated_at: "2026-11-27T00:00:00Z",
        },
      ],
    });

    render(<RolesPage />);

    await waitFor(() => expect(screen.getByText("Auditor")).toBeTruthy());
    expect(screen.getByText("Read-only compliance reviewer")).toBeTruthy();
    expect(screen.getByText("audit.read")).toBeTruthy();
  });

  it("shows empty state and opens the create role dialog", async () => {
    getMock.mockResolvedValue({ success: true, data: [] });

    render(<RolesPage />);

    await waitFor(() => expect(screen.getByRole("button", { name: /roles\.new_role/i })).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: /roles\.new_role/i }));
    await waitFor(() => expect(screen.getByText("roles.create_title")).toBeTruthy());
  });
});
