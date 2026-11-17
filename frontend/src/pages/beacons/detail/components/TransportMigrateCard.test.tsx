import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import TransportMigrateCard, { validateTransportSelection } from "./TransportMigrateCard";

const mocks = vi.hoisted(() => ({ postJson: vi.fn() }));

vi.mock("@/lib/api", () => ({
  api: { postJson: mocks.postJson },
  formatThrownError: (err: unknown) => String(err),
}));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));

describe("validateTransportSelection", () => {
  it("accepts a transport with a configured endpoint", () => {
    expect(validateTransportSelection("tcp", "")).toBeNull();
    expect(validateTransportSelection("udp", "")).toBeNull();
  });

  it("accepts matching absolute URLs, including the family aliases", () => {
    expect(validateTransportSelection("tcp", "tcp://host:4444")).toBeNull();
    expect(validateTransportSelection("tcp", "tls://host:4444")).toBeNull();
    expect(validateTransportSelection("http", "https://c2.example/collect")).toBeNull();
    expect(validateTransportSelection("wss", "wss://c2.example/ws")).toBeNull();
  });

  it("refuses unsupported transports and invalid URLs", () => {
    expect(validateTransportSelection("nope", "")).toBe("unsupported");
    expect(validateTransportSelection("tcp", "not-a-url")).toBe("invalid");
    expect(validateTransportSelection("tcp", "tcp://")).toBe("invalid");
  });

  it("refuses a URL whose scheme the transport cannot dial", () => {
    expect(validateTransportSelection("tcp", "https://c2.example/x")).toBe("mismatch");
    expect(validateTransportSelection("http", "tcp://host:4444")).toBe("mismatch");
    expect(validateTransportSelection("udp", "quic://host:443")).toBe("mismatch");
  });
});

describe("TransportMigrateCard", () => {
  beforeEach(() => mocks.postJson.mockClear());

  it("posts the selected transport and URL to the agent migration route", async () => {
    render(<TransportMigrateCard agentId="agent-9" online />);
    fireEvent.change(screen.getByLabelText("agents.detail_transport_select"), { target: { value: "tcp" } });
    fireEvent.change(screen.getByLabelText("agents.detail_transport_url"), { target: { value: "tcp://host:4444" } });
    fireEvent.click(screen.getByRole("button", { name: "agents.detail_transport_queue" }));
    await waitFor(() => expect(mocks.postJson).toHaveBeenCalledWith("/agents/agent-9/transport", {
      transport: "tcp",
      c2_url: "tcp://host:4444",
    }));
    expect(await screen.findByText("agents.detail_transport_queued")).toBeTruthy();
  });

  it("does not send an invalid selection", () => {
    render(<TransportMigrateCard agentId="agent-9" online />);
    fireEvent.change(screen.getByLabelText("agents.detail_transport_url"), { target: { value: "https://c2.example" } });
    fireEvent.click(screen.getByRole("button", { name: "agents.detail_transport_queue" }));
    expect(mocks.postJson).not.toHaveBeenCalled();
  });

  it("disables queueing for an offline agent", () => {
    render(<TransportMigrateCard agentId="agent-9" online={false} />);
    expect((screen.getByRole("button", { name: "agents.detail_transport_queue" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
