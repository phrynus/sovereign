import { describe, expect, it } from "vitest";
import { agentDetailHref, buildAgentCopyText, buildAgentMarkdown } from "./agent-detail-utils";

describe("agentDetailHref", () => {
  it("builds the canonical agent session path", () => {
    expect(agentDetailHref("eac1558c-3a53-41e1-9498-6e6ded52d6ae"))
      .toBe("/beacons/eac1558c-3a53-41e1-9498-6e6ded52d6ae");
  });

describe("agent task exports", () => {
  const data = {
    agent: { id: "a1", hostname: "host1" },
    total_tasks: 12,
    completed_tasks: 8,
    pending_tasks: 3,
    failed_tasks: 1,
  };

  it("keeps the existing status totals for a full projection", () => {
    expect(buildAgentMarkdown(data)).toContain("12 (8 completed, 3 pending, 1 failed)");
    expect(buildAgentCopyText(data)).toContain("Tasks: 12 (8 completed, 3 pending, 1 failed)");
  });

  it("exports only the explicit total when task contents are redacted", () => {
    const redacted = { ...data, tasks_redacted: true };
    expect(buildAgentMarkdown(redacted)).toContain("| Tasks | 12 |");
    expect(buildAgentCopyText(redacted)).toBe("Agent: host1 (a1)\nOS: — —\nIP: —\nUser: —\nStatus: offline\nUptime: —\nTasks: 12");
    expect(buildAgentMarkdown(redacted)).not.toContain("completed");
    expect(buildAgentCopyText(redacted)).not.toContain("failed");
  });
});
});
