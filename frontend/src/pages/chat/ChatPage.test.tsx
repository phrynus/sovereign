import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import ChatPage from "./ChatPage";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  postJson: vi.fn(),
  post: vi.fn(),
  ws: { connected: false },
  chatFrame: undefined as undefined | ((event: Record<string, unknown>) => void),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  registerFragment: vi.fn(),
  getLocale: () => "en",
}));

vi.mock("@/lib/api", () => ({
  api: { get: mocks.get, postJson: mocks.postJson, post: mocks.post },
  getRateLimitRetryAfter: () => 0,
}));

vi.mock("@/lib/useWebSocket", () => ({
  useWebSocket: () => ({
    connected: mocks.ws.connected,
    reconnectFailed: false,
    demoMode: false,
    subscribe: () => () => {},
    reconnect: vi.fn(),
  }),
}));

vi.mock("@/lib/typed-ws", () => ({
  useTypedWS: (_name: string, cb: (event: Record<string, unknown>) => void) => {
    mocks.chatFrame = cb;
  },
}));

const message = (id: number, body: string, extra: Record<string, unknown> = {}) => ({
  id,
  engagement_id: 1,
  channel_id: 10,
  user_id: 2,
  username: "operator",
  message: body,
  kind: "user",
  metadata: "",
  created_at: `2026-11-24T00:00:${String(id).padStart(2, "0")}Z`,
  ...extra,
});

function installApi() {
  mocks.get.mockImplementation((path: string) => {
    if (path === "/api/engagements") {
      return Promise.resolve({
        engagements: [
          { id: 1, name: "Alpha Ops", state: "active" },
          { id: 2, name: "Bravo Ops", state: "scheduled" },
        ],
      });
    }
    if (path.startsWith("/chat/channels")) {
      const engagement = new URL(`http://local${path}`).searchParams.get("engagement_id");
      if (engagement === "2") {
        return Promise.resolve({
          channels: [{ id: 20, name: "General", slug: "general", kind: "general", archived: false, muted: false, unread_count: 0 }],
        });
      }
      return Promise.resolve({
        channels: [
          { id: 10, name: "General", slug: "general", kind: "general", archived: false, muted: false, unread_count: 2 },
          { id: 11, name: "Approvals", slug: "approvals", kind: "approvals", archived: false, muted: false, unread_count: 0 },
          { id: 12, name: "Ops", slug: "ops", kind: "custom", archived: false, muted: false, unread_count: 0 },
        ],
      });
    }
    if (path.startsWith("/chat/history")) {
      const params = new URL(`http://local${path}`).searchParams;
      const channel = params.get("channel_id");
      if (channel === "11") {
        return Promise.resolve({ messages: [message(7, "approvals message", { channel_id: 11 })], next_before_id: 0 });
      }
      if (channel === "12") {
        return Promise.resolve({ messages: [message(10, "ops message", { channel_id: 12 })], next_before_id: 5 });
      }
      if (params.get("engagement_id") === "2") {
        return Promise.resolve({ messages: [message(8, "bravo message", { engagement_id: 2, channel_id: 20 })], next_before_id: 0 });
      }
      return Promise.resolve({
        messages: [
          message(6, "Task 77 awaits approval", { kind: "approval", task_id: 77 }),
          message(5, "alpha message"),
        ],
        next_before_id: 0,
      });
    }
    return Promise.resolve({});
  });
  mocks.postJson.mockResolvedValue({});
  mocks.post.mockResolvedValue({});
}

describe("ChatPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ws.connected = false;
    mocks.chatFrame = undefined;
    installApi();
  });

  afterEach(() => {
    cleanup();
  });

  it("loads the engagement's channels and history", async () => {
    render(<ChatPage />);
    expect(await screen.findByText("alpha message")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Alpha Ops/ }).getAttribute("aria-current")).toBe("true");
    expect(screen.getByRole("button", { name: /Approvals/ })).toBeTruthy();
  });

  it("clears the old transcript when the engagement or channel changes", async () => {
    render(<ChatPage />);
    expect(await screen.findByText("alpha message")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Bravo Ops/ }));
    expect(await screen.findByText("bravo message")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("alpha message")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: /Alpha Ops/ }));
    expect(await screen.findByText("alpha message")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Approvals/ }));
    expect(await screen.findByText("approvals message")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("alpha message")).toBeNull());
  });

  it("refetches on reconnect and dedupes live frames by message id", async () => {
    const view = render(<ChatPage />);
    expect(await screen.findByText("alpha message")).toBeTruthy();
    const beforeReconnect = mocks.get.mock.calls.filter((call) => String(call[0]).startsWith("/chat/history")).length;

    act(() => {
      mocks.ws.connected = true;
      view.rerender(<ChatPage />);
    });
    await waitFor(() => {
      const calls = mocks.get.mock.calls.filter((call) => String(call[0]).startsWith("/chat/history"));
      expect(calls.length).toBeGreaterThan(beforeReconnect);
    });
    await waitFor(() => expect(screen.getAllByText("alpha message")).toHaveLength(1));

    act(() => {
      mocks.chatFrame?.({ type: "chat", id: 5, engagement_id: 1, channel_id: 10, username: "operator", message: "alpha message", kind: "user" });
      mocks.chatFrame?.({ type: "chat", id: 9, engagement_id: 1, channel_id: 10, username: "operator", message: "live message", kind: "user" });
      mocks.chatFrame?.({ type: "chat", id: 99, engagement_id: 2, channel_id: 20, username: "foreign", message: "foreign message", kind: "user" });
    });
    expect(screen.getByText("live message")).toBeTruthy();
    expect(screen.queryByText("foreign message")).toBeNull();
    await waitFor(() => expect(screen.getAllByText("alpha message")).toHaveLength(1));
  });

  it("calls the existing task approve and reject endpoints from approval cards", async () => {
    render(<ChatPage />);
    const card = await screen.findByTestId("approval-77");
    fireEvent.click(within(card).getByRole("button", { name: "chat.approve" }));
    await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("/tasks/77/approve"));
    fireEvent.click(within(card).getByRole("button", { name: "chat.reject" }));
    await waitFor(() => expect(mocks.post).toHaveBeenCalledWith("/tasks/77/reject"));
  });

  it("uses keyset cursors and server-side search", async () => {
    render(<ChatPage />);
    expect(await screen.findByText("alpha message")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Opscustom" }));
    expect(await screen.findByText("ops message")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "chat.load_older" }));
    await waitFor(() => expect(mocks.get.mock.calls.some((call) => String(call[0]).includes("before_id=5"))).toBe(true));

    fireEvent.change(screen.getByRole("textbox", { name: "chat.search_placeholder" }), { target: { value: "needle" } });
    fireEvent.click(screen.getByRole("button", { name: "chat.search" }));
    await waitFor(() => expect(mocks.get.mock.calls.some((call) => String(call[0]).includes("q=needle"))).toBe(true));
  });

  it("sends plain text and exposes channel controls", async () => {
    render(<ChatPage />);
    expect(await screen.findByText("alpha message")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: "chat.message_placeholder" }), { target: { value: "<b>not html</b>" } });
    fireEvent.click(screen.getByRole("button", { name: "chat.send" }));
    await waitFor(() => expect(mocks.postJson).toHaveBeenCalledWith("/chat/send", {
      engagement_id: 1,
      channel_id: 10,
      message: "<b>not html</b>",
    }));
    expect(screen.queryByText("not html")).toBeNull();
    await waitFor(() => expect(mocks.postJson).toHaveBeenCalledWith("/chat/channels/10/read", { message_id: 6 }));

    fireEvent.click(screen.getByRole("button", { name: "chat.mute" }));
    await waitFor(() => expect(mocks.postJson).toHaveBeenCalledWith("/chat/channels/10/mute", { muted: true }));

    fireEvent.click(screen.getByRole("button", { name: "chat.create_channel" }));
    fireEvent.change(screen.getByRole("textbox", { name: "chat.channel_name" }), { target: { value: "War room" } });
    fireEvent.click(screen.getByRole("button", { name: "chat.create" }));
    await waitFor(() => expect(mocks.postJson).toHaveBeenCalledWith("/chat/channels", {
      engagement_id: 1,
      name: "War room",
    }));
  });
});
