import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import PasswordSprayPanel from "./PasswordSprayPanel";

const liveRun = vi.hoisted(() => vi.fn(async () => ({ id: 42, status: "completed", result: "" })));

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

vi.mock("@/lib/hooks/useLiveTaskResult", () => ({
  useLiveTaskResult: () => ({ run: liveRun, running: false, error: "" }),
}));

const getMock = vi.mocked(api.get);

beforeEach(() => {
  vi.clearAllMocks();
  liveRun.mockResolvedValue({ id: 42, status: "completed", result: "" } as never);
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

async function chooseVaultCredential() {
  fireEvent.focus(screen.getByTestId("credential-picker-search"));
  await screen.findByTestId("credential-picker-option-7");
  fireEvent.change(screen.getByTestId("credential-picker-select"), { target: { value: "7" } });
}

function fillUsernames() {
  fireEvent.change(screen.getByPlaceholderText("spray.usernames_placeholder"), { target: { value: "alice\nbob\n" } });
}

describe("PasswordSprayPanel credential references", () => {
  it("submits only credential_id and non-sensitive parameters in Vault mode", async () => {
    render(<PasswordSprayPanel selectedAgent="a1" />);
    fireEvent.click(screen.getByTestId("spray-mode-vault"));
    await chooseVaultCredential();
    fillUsernames();

    fireEvent.click(screen.getByRole("button", { name: "spray.execute" }));

    await waitFor(() => expect(liveRun).toHaveBeenCalledTimes(1));
    const [agent, path, body] = liveRun.mock.calls[0] as unknown as [string, string, Record<string, string>];
    expect(agent).toBe("a1");
    expect(path).toBe(paths.agents.cmd("a1", "password_spray"));
    expect(body.credential_id).toBe("7");
    expect(body.usernames).toBe("alice\nbob");
    expect(body).not.toHaveProperty("password");
    expect(body).not.toHaveProperty("domain");
    expect(body).not.toHaveProperty("user");
    expect(screen.queryByLabelText(/spray\.password/)).toBeNull();
    for (const call of getMock.mock.calls) {
      expect(String(call[0])).not.toContain(paths.credentials.list());
    }
  });

  it("keeps inline password submission in Manual mode", async () => {
    render(<PasswordSprayPanel selectedAgent="a1" />);
    fireEvent.change(screen.getByLabelText(/spray\.domain/), { target: { value: "CORP" } });
    fireEvent.change(screen.getByLabelText(/spray\.password/), { target: { value: "P@ssw0rd!" } });
    fillUsernames();

    fireEvent.click(screen.getByRole("button", { name: "spray.execute" }));

    await waitFor(() => expect(liveRun).toHaveBeenCalledTimes(1));
    const body = (liveRun.mock.calls[0] as unknown as [string, string, Record<string, string>])[2];
    expect(body.password).toBe("P@ssw0rd!");
    expect(body.domain).toBe("CORP");
    expect(body).not.toHaveProperty("credential_id");
  });
});
