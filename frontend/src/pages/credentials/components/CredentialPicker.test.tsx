import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import CredentialPicker from "./CredentialPicker";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

const OPTIONS = [
  {
    id: 7,
    label: "CORP\\alice",
    domain: "CORP",
    username: "alice",
    type: "cleartext",
    subtype: "",
    expiry_state: "unknown" as const,
    expires_at: null,
  },
  {
    id: 9,
    label: "svc_backup",
    domain: "CORP",
    username: "svc_backup",
    type: "password",
    subtype: "jwt",
    expiry_state: "expired" as const,
    expires_at: "2026-01-01T00:00:00Z",
  },
];

const getMock = vi.mocked(api.get);

beforeEach(() => {
  vi.clearAllMocks();
  getMock.mockResolvedValue({ options: OPTIONS });
});

describe("CredentialPicker", () => {
  it("fetches redacted options on open without requesting plaintext vault entries", async () => {
    const onSelect = vi.fn();
    render(<CredentialPicker selectedId={null} onSelect={onSelect} />);

    fireEvent.focus(screen.getByTestId("credential-picker-search"));

    await waitFor(() => expect(getMock).toHaveBeenCalledWith(`${paths.credentials.options}?q=&limit=100`));
    const option = await screen.findByTestId("credential-picker-option-7");
    expect(option.textContent).toContain("CORP\\alice");
    expect(option.textContent).toContain("cleartext");
    expect(option.textContent).toContain("unknown");
    for (const call of getMock.mock.calls) {
      expect(String(call[0])).not.toContain(paths.credentials.list());
      expect(String(call[0])).not.toContain("password=");
    }
  });

  it("debounces the query before reloading options", async () => {
    render(<CredentialPicker selectedId={null} onSelect={vi.fn()} />);
    fireEvent.change(screen.getByTestId("credential-picker-search"), { target: { value: "alice" } });
    expect(getMock).not.toHaveBeenCalledWith(`${paths.credentials.options}?q=alice&limit=100`);

    await waitFor(() => expect(getMock).toHaveBeenCalledWith(`${paths.credentials.options}?q=alice&limit=100`), { timeout: 2000 });
  });

  it("shows an unknown expiry badge as unknown, never as expired", async () => {
    const onSelect = vi.fn();
    const view = render(<CredentialPicker selectedId={null} onSelect={onSelect} />);
    fireEvent.focus(screen.getByTestId("credential-picker-search"));
    await screen.findByTestId("credential-picker-option-7");

    fireEvent.change(screen.getByTestId("credential-picker-select"), { target: { value: "7" } });
    expect(onSelect).toHaveBeenCalledWith(7);
    view.rerender(<CredentialPicker selectedId={7} onSelect={onSelect} />);
    const badge = await screen.findByTestId("credential-picker-expiry");
    expect(badge.getAttribute("data-expiry-state")).toBe("unknown");
    expect(badge.textContent).toContain("cred.expiry_unknown");
    expect(badge.textContent).not.toContain("cred.expiry_expired");
    // The secret is described as held in the vault, never rendered as a field.
    expect(screen.getByTestId("credential-picker-secret-hint").textContent).toContain("cred.picker.secret_hint");
    expect(screen.queryByLabelText("Password")).toBeNull();
  });

  it("filters options by allowed types", async () => {
    render(<CredentialPicker selectedId={null} onSelect={vi.fn()} allowedTypes={["jwt"]} />);
    fireEvent.focus(screen.getByTestId("credential-picker-search"));
    await screen.findByTestId("credential-picker-option-9");
    expect(screen.queryByTestId("credential-picker-option-7")).toBeNull();
  });
});
