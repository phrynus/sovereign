import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AgentHeader from "./AgentHeader";
import type { PermissionKey } from "@/lib/permission-keys";

vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));
let permissions: PermissionKey[] | null = null;
const can = vi.fn((permission: PermissionKey) => permissions?.includes(permission) ?? false);
vi.mock("@/lib/hooks/usePermissions", () => ({ usePermissions: () => ({ can, permissions }) }));

function renderHeader() {
  return render(
    <MemoryRouter>
      <AgentHeader
        agent={{ hostname: "host-1", ip: "10.0.0.1", os: "windows" }}
        agentId="11111111-2222-4333-8444-555555555555"
        status="online"
        actionLoading={null}
        onQuickAction={vi.fn()}
        credCount={0}
        onKill={vi.fn()}
        onUninstall={vi.fn()}
      />
    </MemoryRouter>,
  );
}

async function openMore() {
  fireEvent.click(screen.getByRole("button", { name: /agents\.header_more/ }));
}

describe("AgentHeader operation-log link", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    can.mockImplementation((permission: PermissionKey) => permissions?.includes(permission) ?? false);
    permissions = null;
  });

  it("opens the beacon's operations log when audit.read is granted", async () => {
    can.mockReturnValue(true);
    renderHeader();
    await openMore();

    const item = await screen.findByRole("menuitem", { name: /agents\.header_operations/ });
    expect(item.getAttribute("href")).toBe(
      "/audit?tab=operations&agent_id=11111111-2222-4333-8444-555555555555",
    );
    expect(can).toHaveBeenCalledWith("audit.read");
  });

  it("hides the link without audit.read", async () => {
    can.mockReturnValue(false);
    renderHeader();
    await openMore();

    expect(screen.queryByRole("menuitem", { name: /agents\.header_operations/ })).toBeNull();
  });

  it("hides the files entry without files.read", () => {
    permissions = ["agents.read"];
    renderHeader();
    expect(screen.queryByRole("link", { name: /agents\.files_title/ })).toBeNull();
  });

  it("shows the files entry to file-only operators", () => {
    permissions = ["files.read"];
    renderHeader();
    expect(screen.getByRole("link", { name: /agents\.files_title/ }).getAttribute("href")).toContain("/files");
  });
});
