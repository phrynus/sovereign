import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import { useAppStore } from "@/lib/store";
import type { PermissionKey } from "@/lib/permission-keys";
import type { TransferSession } from "@/lib/agent-files/file-transfer";
import FilesToolbar from "./FilesToolbar";
import FilesTable from "./FilesTable";
import FilesFindPanel from "./FilesFindPanel";
import TransferList from "./TransferList";

const t = (key: string) => key;
const entry = { name: "note.txt", is_dir: false, size: 12, mod_time: "" };

function setPermissions(permissions: PermissionKey[]) {
  useAppStore.setState({ currentPermissions: permissions });
}

function tableProps() {
  return {
    t,
    sortedEntries: [entry],
    selected: new Set<string>(),
    allSelected: false,
    toggleSelect: vi.fn(),
    selectAll: vi.fn(),
    clearSelection: vi.fn(),
    sortKey: "name" as const,
    sortDir: 1 as const,
    toggleSort: vi.fn(),
    selectedFile: null,
    batchBusy: false,
    pullName: null,
    onFileClick: vi.fn(),
    onEdit: vi.fn(),
    onPreview: vi.fn(),
    onDownload: vi.fn(),
    onRename: vi.fn(),
    onChmod: vi.fn(),
    onDelete: vi.fn(),
    onBatchPull: vi.fn(),
    onBatchDelete: vi.fn(),
    onCancelBatch: vi.fn(),
    dragOver: false,
    onDragOver: vi.fn(),
    onDragLeave: vi.fn(),
    onDrop: vi.fn(),
  };
}

function session(overrides: Partial<TransferSession>): TransferSession {
  return {
    id: 1,
    direction: "pull",
    remote_path: "/tmp/note.txt",
    filename: "note.txt",
    total_size: 12,
    committed_offset: 0,
    status: "paused",
    ...overrides,
  };
}

function transferProps(transfers: TransferSession[]) {
  return {
    t,
    transfers,
    busyId: null,
    onPause: vi.fn(),
    onResume: vi.fn(),
    onCancel: vi.fn(),
    onStartOver: vi.fn(),
    onDownload: vi.fn(),
  };
}

afterEach(() => {
  useAppStore.setState({ currentPermissions: null });
});

describe("file permissions", () => {
  it("shows file-only operators the read toolbar without write actions", () => {
    setPermissions(["files.read"]);
    render(<FilesToolbar t={t} agentId="a1" onToggleFind={vi.fn()} onUpload={vi.fn()} onDrives={vi.fn()} onMkdir={vi.fn()} onUsb={vi.fn()} onRefresh={vi.fn()} />);

    expect(screen.getByRole("button", { name: "agents.files_find" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_drives" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_usb" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "agents.files_upload" })).toBeNull();
    expect(screen.queryByRole("button", { name: "agents.files_mkdir" })).toBeNull();
  });

  it.each([["files.write"], ["agents.read"]])("hides the file toolbar without files.read (%s)", (permission) => {
    setPermissions([permission as PermissionKey]);
    const { container } = render(<FilesToolbar t={t} agentId="a1" onToggleFind={vi.fn()} onUpload={vi.fn()} onDrives={vi.fn()} onMkdir={vi.fn()} onUsb={vi.fn()} onRefresh={vi.fn()} />);
    expect(container.textContent).toBe("");
  });

  it("keeps preview and pull available to file-only operators while hiding mutations", () => {
    setPermissions(["files.read"]);
    render(<FilesTable {...tableProps()} />);

    expect(screen.getByRole("button", { name: "agents.files_preview_btn" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_pull" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "agents.files_edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "agents.files_rename" })).toBeNull();
    expect(screen.queryByRole("button", { name: "agents.files_chmod" })).toBeNull();
    expect(screen.queryByRole("button", { name: "common.delete" })).toBeNull();
  });

  it("shows mutation controls when files.write is also granted", () => {
    setPermissions(["files.read", "files.write"]);
    render(<FilesTable {...tableProps()} />);

    expect(screen.getByRole("button", { name: "agents.files_edit" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_rename" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_chmod" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "common.delete" })).toBeTruthy();
  });

  it("keeps filename search readable but gates deep hunt behind files.write", () => {
    setPermissions(["files.read"]);
    const props = {
      t, sep: "/", currentPath: "/tmp", findPattern: "*", setFindPattern: vi.fn(),
      findResults: [], setFindResults: vi.fn(), huntDownload: false, setHuntDownload: vi.fn(),
      findFiles: vi.fn(), huntFiles: vi.fn(), navigateTo: vi.fn(), setSelectedFile: vi.fn(),
    };
    const { rerender } = render(<FilesFindPanel {...props} />);
    expect(screen.getByRole("button", { name: "common.search" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "agents.files_hunt" })).toBeNull();
    expect(screen.queryByRole("button", { name: "agents.files_hunt_download" })).toBeNull();

    act(() => setPermissions(["files.read", "files.write"]));
    rerender(<FilesFindPanel {...props} />);
    expect(screen.getByRole("button", { name: "agents.files_hunt" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "agents.files_hunt_download" })).toBeTruthy();
  });

  it("gates pull transfer controls with files.read and push controls with files.write", () => {
    setPermissions(["files.read"]);
    render(<TransferList {...transferProps([
      session({ id: 1, filename: "pull.bin" }),
      session({ id: 2, filename: "pull-failed.bin", status: "failed" }),
      session({ id: 3, filename: "pull-done.bin", status: "completed" }),
      session({ id: 4, filename: "push.bin", direction: "push", status: "paused" }),
    ])} />);

    const pull = within(screen.getByText("pull.bin").parentElement!.parentElement!);
    const pullFailed = within(screen.getByText("pull-failed.bin").parentElement!.parentElement!);
    const pullDone = within(screen.getByText("pull-done.bin").parentElement!.parentElement!);
    const push = within(screen.getByText("push.bin").parentElement!.parentElement!);
    expect(pull.getByRole("button", { name: "agents.files_transfer_resume" })).toBeTruthy();
    expect(pullFailed.getByRole("button", { name: "agents.files_transfer_start_over" })).toBeTruthy();
    expect(pullDone.getByRole("button", { name: "agents.files_transfer_download" })).toBeTruthy();
    expect(pull.queryByRole("button", { name: "agents.files_transfer_cancel" })).toBeNull();
    expect(push.queryByRole("button", { name: "agents.files_transfer_resume" })).toBeNull();
    expect(push.queryByRole("button", { name: "agents.files_transfer_cancel" })).toBeNull();
  });

  it.each([["files.write"], ["agents.read"]])("hides transfer controls without files.read (%s)", (permission) => {
    setPermissions([permission as PermissionKey]);
    const { container } = render(<TransferList {...transferProps([session({})])} />);
    expect(container.textContent).toBe("");
  });
});
