import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import FilesEditor from "./FilesEditor";
import { useFileEditor } from "./useFileEditor";
import { useConfirm } from "@/lib/hooks/useConfirm";

vi.mock("./useFileEditor", () => ({ useFileEditor: vi.fn() }));
vi.mock("@/lib/hooks/useConfirm", () => ({ useConfirm: vi.fn() }));
vi.mock("sonner", () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }));

const t = (key: string) => key;
const save = vi.fn().mockResolvedValue(undefined);
const reload = vi.fn().mockResolvedValue(undefined);
const overwrite = vi.fn().mockResolvedValue(undefined);
const loadHistory = vi.fn().mockResolvedValue([]);
const viewRevision = vi.fn().mockResolvedValue(undefined);

function editorState(overrides: Record<string, unknown> = {}) {
  return {
    draft: "original", dirty: false, loading: false, saving: false,
    conflict: false, error: null, baselineTaskId: 11, baselineHash: "a".repeat(64),
    mtime: 1, revisions: [], revisionText: null,
    changeDraft: vi.fn(), open: vi.fn().mockResolvedValue(undefined), save,
    reload, overwrite, loadHistory, viewRevision, clearRevision: vi.fn(),
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  save.mockResolvedValue(undefined);
  vi.mocked(useConfirm).mockReturnValue({ confirm: vi.fn().mockResolvedValue(true), modal: null } as never);
});

function renderEditor(overrides: Record<string, unknown> = {}) {
  vi.mocked(useFileEditor).mockReturnValue(editorState(overrides) as never);
  return render(
    <FilesEditor t={t} agentId="a1" path="C:\\note.txt" filename="note.txt" onClose={vi.fn()} />,
  );
}

describe("FilesEditor", () => {
  it("opens the baseline and only enables Save when the draft is dirty", () => {
    renderEditor();
    expect(vi.mocked(useFileEditor).mock.results[0].value.open).toHaveBeenCalled();
    const saveButton = screen.getByRole("button", { name: "agents.files_editor_save" });
    expect(saveButton).toHaveProperty("disabled", true);
  });

  it("saves the retained draft and reports success", async () => {
    renderEditor({ dirty: true, draft: "edited" });
    fireEvent.click(screen.getByRole("button", { name: "agents.files_editor_save" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(toast.success).toHaveBeenCalled();
  });

  it("offers Reload and confirmed Overwrite on a conflict", async () => {
    renderEditor({ conflict: true, dirty: true });
    fireEvent.click(screen.getByRole("button", { name: "agents.files_editor_reload" }));
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "agents.files_editor_overwrite" }));
    await waitFor(() => expect(overwrite).toHaveBeenCalledTimes(1));
  });

  it("shows read-only revision history", async () => {
    renderEditor({
      revisions: [{
        task_id: 9, type: "read", operator: "alice", created_at: "2026-01-01T00:00:00Z",
        sha256: "b".repeat(64), mtime: 1, size: 5,
      }],
    });
    fireEvent.click(screen.getByRole("button", { name: "agents.files_editor_history" }));
    await waitFor(() => expect(loadHistory).toHaveBeenCalled());
    expect(screen.getByText(/#9 read/)).toBeTruthy();
  });
});
