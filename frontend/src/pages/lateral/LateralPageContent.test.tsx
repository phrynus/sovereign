import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { api } from "@/lib/api";
import LateralPageContent from "./LateralPageContent";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/lib/hooks/useApiResource", () => ({
  useApiResource: () => ({
    data: { stats: {}, agents: [{ id: "a1", hostname: "agent-A" }], credentials: [], history: [] },
    loading: false,
    error: null,
    refresh: vi.fn(),
  }),
}));

const getMock = vi.mocked(api.get);
const postJsonMock = vi.mocked(api.postJson);

beforeEach(() => {
  vi.clearAllMocks();
  postJsonMock.mockResolvedValue({ task_id: 1 });
  getMock.mockImplementation(async (path: unknown) => {
    if (String(path).includes("/api/credentials/options")) {
      return {
        options: [{
          id: 7,
          label: "CORP\\alice",
          domain: "CORP",
          username: "alice",
          type: "cleartext",
          subtype: "",
          expiry_state: "unknown",
          expires_at: null,
        }],
      };
    }
    return {};
  });
});

async function chooseSourceAgent() {
  fireEvent.mouseDown(screen.getAllByRole("combobox")[0], { button: 0 });
  fireEvent.click(await screen.findByRole("option", { name: "agent-A" }));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

function fillTarget() {
  fireEvent.change(screen.getByLabelText("lateral.ip_or_hostname"), { target: { value: "10.0.0.5" } });
}

describe("LateralPageContent credential references", () => {
  it("sends a numeric credential_id and no inline secrets in Vault mode", async () => {
    render(<LateralPageContent />);
    await chooseSourceAgent();
    fillTarget();
    fireEvent.click(screen.getByTestId("lateral-mode-vault"));

    fireEvent.focus(screen.getByTestId("credential-picker-search"));
    await screen.findByTestId("credential-picker-option-7");
    fireEvent.change(screen.getByTestId("credential-picker-select"), { target: { value: "7" } });

    expect(screen.queryByLabelText("lateral.a11y_password")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "lateral.execute_lateral" }));

    await waitFor(() => expect(postJsonMock).toHaveBeenCalledTimes(1));
    const [path, payload] = postJsonMock.mock.calls[0] as [string, Record<string, unknown>];
    expect(path).toBe("/api/lateral/execute");
    expect(payload.credential_id).toBe(7);
    expect(payload.target).toBe("10.0.0.5");
    expect(payload).not.toHaveProperty("password");
    expect(payload).not.toHaveProperty("username");
    expect(payload).not.toHaveProperty("hash");
    expect(payload).not.toHaveProperty("credential");
  });

  it("keeps inline password submission in Manual mode", async () => {
    render(<LateralPageContent />);
    await chooseSourceAgent();
    fillTarget();
    fireEvent.change(screen.getByLabelText("lateral.a11y_password"), { target: { value: "P@ssw0rd!" } });
    fireEvent.change(screen.getByLabelText("lateral.a11y_username"), { target: { value: "alice" } });

    fireEvent.click(screen.getByRole("button", { name: "lateral.execute_lateral" }));

    await waitFor(() => expect(postJsonMock).toHaveBeenCalledTimes(1));
    const payload = (postJsonMock.mock.calls[0] as [string, Record<string, unknown>])[1];
    expect(payload.password).toBe("P@ssw0rd!");
    expect(payload.username).toBe("alice");
    expect(payload).not.toHaveProperty("credential_id");
  });
});
