import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import TimelineSection from "./TimelineSection";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("@/lib/api", () => ({
  api: { get: getMock },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }) };
});

describe("TimelineSection", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("shows the unreadable banner when the timeline read fails", async () => {
    getMock.mockRejectedValue({ status: 500, message: "timeline exploded" });
    render(<TimelineSection agentId="a1" online />);

    // The banner replaces the reassuring "no history" line, which would read
    // as a fact about the agent rather than about the request.
    expect(await screen.findByText("agents.timeline_unreadable")).toBeTruthy();
  });

  it("renders fetched events on a successful read", async () => {
    getMock.mockResolvedValue({
      events: [{ time: new Date().toISOString(), kind: "task", title: "ran whoami" }],
    });
    render(<TimelineSection agentId="a1" online />);

    expect(await screen.findByText("ran whoami")).toBeTruthy();
  });

  it("keeps task title, detail and type for an authorized projection", async () => {
    getMock.mockResolvedValue({
      events: [{
        time: "2026-08-14T00:00:00Z",
        kind: "task",
        title: "ran whoami",
        detail: "operator detail",
        type: "shell",
        status: "completed",
        ref_id: 12,
      }],
    });
    render(<TimelineSection agentId="a1" online />);

    expect(await screen.findByText("ran whoami")).toBeTruthy();
    expect(screen.getByText(/operator detail/)).toBeTruthy();
    expect(screen.getByText(/shell/)).toBeTruthy();
  });

  it("does not read or render task contents in a redacted timeline", async () => {
    const readTitle = vi.fn(() => "SECRET_TITLE_MARKER");
    const readDetail = vi.fn(() => "SECRET_DETAIL_MARKER");
    const readType = vi.fn(() => "SECRET_TYPE_MARKER");
    getMock.mockResolvedValue({
      tasks_redacted: true,
      events: [{
        time: "2026-08-14T00:00:00Z",
        kind: "task",
        status: "completed",
        ref_id: 12,
        get title() { return readTitle(); },
        get detail() { return readDetail(); },
        get type() { return readType(); },
      }],
    });
    const { container } = render(<TimelineSection agentId="a1" online />);

    expect(await screen.findByText("#12")).toBeTruthy();
    expect(screen.getByText("completed")).toBeTruthy();
    expect(container.textContent).not.toContain("SECRET_");
    expect(readTitle).not.toHaveBeenCalled();
    expect(readDetail).not.toHaveBeenCalled();
    expect(readType).not.toHaveBeenCalled();
    expect(getMock).toHaveBeenCalledTimes(1);
  });
});
