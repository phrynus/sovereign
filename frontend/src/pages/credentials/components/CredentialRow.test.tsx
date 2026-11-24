import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "@/components/ui/tooltip";
import { CredentialRow } from "./CredentialRow";
import type { VaultEntry } from "./types";

const entry: VaultEntry = {
  id: "1",
  domain: "EXAMPLE.TEST",
  username: "alice",
  password: "",
  hash: "",
  type: "kerberos",
  subtype: "ccache",
  source: "manual",
  tags: "",
  expires_at: "2026-11-24T12:00:00Z",
  renew_until: "2026-12-01T12:00:00Z",
  expiry_state: "expired",
  confirmed: false,
  agent_id: "",
  notes: "",
};

function renderRow(value: VaultEntry) {
  return render(
    <TooltipProvider>
      <table>
        <tbody>
          <CredentialRow
            entry={value}
            isSelected={false}
            showPassword={false}
            showHash={false}
            onToggleSelect={vi.fn()}
            onToggleConfirm={vi.fn()}
            onEdit={vi.fn()}
            onDelete={vi.fn()}
            togglePasswordVisibility={vi.fn()}
            toggleHashVisibility={vi.fn()}
            t={(key) => key}
          />
        </tbody>
      </table>
    </TooltipProvider>,
  );
}

describe("CredentialRow expiry metadata", () => {
  it("renders subtype, expiry state, expiry date, and renewal date", () => {
    renderRow(entry);

    expect(screen.getByText("ccache")).toBeTruthy();
    expect(screen.getByText("cred.expiry_expired")).toBeTruthy();
    expect(screen.getByText("2026-11-24")).toBeTruthy();
    const renewal = screen.getByText(/cred.renew_until_label/);
    expect(renewal.textContent).toContain("2026-12-01");
  });

  it("renders no dates when the expiry state is unknown", () => {
    renderRow({ ...entry, expiry_state: "unknown" });

    expect(screen.getByText("cred.expiry_unknown")).toBeTruthy();
    expect(screen.queryByText("2026-11-24")).toBeNull();
    expect(screen.queryByText(/cred.renew_until_label/)).toBeNull();
    expect(screen.queryByText("2026-12-01")).toBeNull();
  });
  it("links each row to the credential-filtered task list", () => {
    renderRow(entry);

    const link = screen.getByTestId("credential-tasks-link-1");
    expect(link.getAttribute("href")).toBe("/events?tab=tasks&credential_id=1");
    expect(link.getAttribute("aria-label")).toBe("cred.view_related_tasks");
  });
});
