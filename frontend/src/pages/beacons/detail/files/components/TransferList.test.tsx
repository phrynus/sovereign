import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { useAppStore } from "@/lib/store";
import type { TransferSession } from "@/lib/agent-files/file-transfer";
import TransferList from "./TransferList";

const t = (key: string) => key;

function transfer(overrides: Partial<TransferSession>): TransferSession {
  return {
    id: 1,
    direction: "push",
    remote_path: "/tmp/file.bin",
    filename: "file.bin",
    total_size: 5,
    committed_offset: 5,
    status: "completed",
    ...overrides,
  };
}

const actions = {
  onPause: vi.fn(),
  onResume: vi.fn(),
  onCancel: vi.fn(),
  onStartOver: vi.fn(),
  onDownload: vi.fn(),
};

beforeEach(() => {
  useAppStore.setState({ currentPermissions: ["files.read", "files.write"] });
});

describe("TransferList", () => {
  it("distinguishes verified, recorded and unavailable checksums", () => {
    render(
      <TransferList
        t={t}
        busyId={null}
        transfers={[
          transfer({ sha256: "a".repeat(64), computed_sha256: "a".repeat(64) }),
          transfer({ id: 2, direction: "pull", computed_sha256: "b".repeat(64) }),
          transfer({ id: 3, direction: "pull" }),
        ]}
        {...actions}
      />,
    );

    expect(screen.getAllByText(/agents\.files_transfer_verified/)).toHaveLength(1);
    expect(screen.getAllByText(/agents\.files_transfer_recorded/)).toHaveLength(1);
    expect(screen.getAllByText("agents.files_transfer_unverified")).toHaveLength(1);
    expect(screen.getAllByText(/agents\.files_transfer_checksum/)).toHaveLength(2);
  });
});
