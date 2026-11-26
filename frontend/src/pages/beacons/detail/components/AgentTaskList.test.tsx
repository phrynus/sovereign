import { describe, it, expect, vi, beforeEach } from "vitest";
import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { formatTime } from "@/lib/utils";
import AgentTaskList from "./AgentTaskList";
import type { TaskEntry } from "./agent-detail-utils";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }));
vi.mock("@/lib/i18n", () => ({ getLocale: () => "en", useI18n: () => ({ t: (key: string) => key }) }));

function renderList(task: TaskEntry, tasksRedacted = false) {
  return render(
    <MemoryRouter>
      <AgentTaskList
        tasks={[task]}
        tasksRedacted={tasksRedacted}
        agentId="a1"
        expandedTaskId={null}
        onToggleExpand={vi.fn()}
        totalTasks={42}
      />
    </MemoryRouter>,
  );
}

function AuthorizedList() {
  const [expanded, setExpanded] = useState<string | number | null>(null);
  return (
    <MemoryRouter>
      <AgentTaskList
        tasks={[{
          id: 1,
          type: "shell",
          command: "whoami",
          status: "completed",
          result: "PREVIEW_MARKER",
          created_at: "2026-08-14T00:00:00Z",
          updated_at: "2026-08-14T00:01:00Z",
        }]}
        tasksRedacted={false}
        agentId="a1"
        expandedTaskId={expanded}
        onToggleExpand={setExpanded}
        totalTasks={42}
      />
    </MemoryRouter>
  );
}

describe("AgentTaskList task projections", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("keeps command and full-result loading for an authorized projection", async () => {
    vi.mocked(api.get).mockResolvedValue({ result: "FULL_RESULT_MARKER" });
    render(<AuthorizedList />);

    expect(screen.getByText("shell")).toBeTruthy();
    expect(screen.getByText("whoami")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "agents.tasklist_toggle" }));

    expect(await screen.findByText("FULL_RESULT_MARKER")).toBeTruthy();
    expect(api.get).toHaveBeenCalledWith(paths.tasks.one(1));
  });

  it("renders only id, status and timestamps without reading or fetching task contents", () => {
    const readType = vi.fn(() => "SECRET_TYPE_MARKER");
    const readCommand = vi.fn(() => "SECRET_COMMAND_MARKER");
    const readResult = vi.fn(() => "SECRET_RESULT_MARKER");
    const readError = vi.fn(() => "SECRET_ERROR_MARKER");
    const readCreator = vi.fn(() => "SECRET_CREATOR_MARKER");
    const task: TaskEntry = {
      id: 7,
      status: "completed",
      created_at: "2026-08-14T00:00:00Z",
      updated_at: "2026-08-14T00:01:00Z",
      get type() { return readType(); },
      get command() { return readCommand(); },
      get result() { return readResult(); },
      get error() { return readError(); },
      get created_by() { return readCreator(); },
    };
    const { container } = renderList(task, true);

    expect(screen.getByText("status.completed")).toBeTruthy();
    expect(screen.getByText("42")).toBeTruthy();
    expect(screen.getByText(formatTime(task.created_at!))).toBeTruthy();
    expect(screen.getByText(formatTime(task.updated_at!))).toBeTruthy();
    expect(container.textContent).not.toContain("SECRET_");
    expect(container.querySelector("pre")).toBeNull();
    expect(screen.queryByRole("button", { name: "agents.tasklist_toggle" })).toBeNull();
    expect(screen.queryByPlaceholderText("agents.tasklist_search_placeholder")).toBeNull();
    expect(screen.queryByText("agents.detail_completed")).toBeNull();
    expect(screen.queryByText("agents.detail_pending")).toBeNull();
    expect(screen.queryByText("agents.detail_failed")).toBeNull();
    expect(readType).not.toHaveBeenCalled();
    expect(readCommand).not.toHaveBeenCalled();
    expect(readResult).not.toHaveBeenCalled();
    expect(readError).not.toHaveBeenCalled();
    expect(readCreator).not.toHaveBeenCalled();
    expect(api.get).not.toHaveBeenCalled();
  });

  it("counts pending approval tasks in the pending backlog", () => {
    renderList({ id: 8, type: "shell", status: "pending_approval", created_at: "2026-08-14T00:00:00Z" });
    const label = screen.getByText("agents.detail_pending");
    expect(label.parentElement?.textContent).toContain("1");
  });
});

