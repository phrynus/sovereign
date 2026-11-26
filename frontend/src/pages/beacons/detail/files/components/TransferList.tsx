import { memo } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Permission } from "@/components/ui/permission";
import { Progress } from "@/components/ui/progress";
import { ArrowDownToLine, ArrowUpToLine, Download, Pause, Play, RotateCcw, X } from "lucide-react";
import { formatBytes } from "@/lib/agent-files/types";
import { transferPercent } from "@/lib/agent-files/file-task";
import type { TransferSession } from "@/lib/agent-files/file-transfer";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface TransferListProps {
  t: TKey;
  transfers: TransferSession[];
  busyId: number | null;
  onPause: (id: number) => void;
  onResume: (session: TransferSession) => void;
  onCancel: (id: number) => void;
  onStartOver: (session: TransferSession) => void;
  onDownload: (session: TransferSession) => void;
}

/** Server-confirmed transfer sessions; progress is committed bytes only. */
export default memo(function TransferList({
  t, transfers, busyId, onPause, onResume, onCancel, onStartOver, onDownload,
}: TransferListProps) {
  if (transfers.length === 0) return null;
  return (
    <Permission perms="files.read">
      <Card className="space-y-2 p-(--card-spacing)">
        <h3 className="text-xs font-medium text-muted-foreground">{t("agents.files_transfers_title")}</h3>
        {transfers.map((session) => {
          const pct = transferPercent({ offset: session.committed_offset, total: session.total_size });
          const busy = busyId === session.id;
          const transferPerm = session.direction === "push" ? "files.write" : "files.read";
          return (
            <div key={session.id} className="space-y-1 border-t border-border/60 pt-2 first:border-0 first:pt-0">
              <div className="flex flex-wrap items-center gap-2 text-xs">
                {session.direction === "push" ? (
                  <ArrowUpToLine className="size-3.5 text-primary" />
                ) : (
                  <ArrowDownToLine className="size-3.5 text-primary" />
                )}
                <span className="min-w-0 flex-1 truncate font-mono">{session.filename || session.remote_path}</span>
                <span className="text-muted-foreground">
                  {t(`agents.files_transfer_status_${session.status}`)}
                </span>
                <span className="font-mono text-muted-foreground">
                  {session.total_size > 0
                    ? t("agents.files_transfer_bytes", {
                        done: formatBytes(session.committed_offset),
                        total: formatBytes(session.total_size),
                        pct,
                      })
                    : t("agents.files_transfer_unknown_total", { done: formatBytes(session.committed_offset) })}
                </span>
              </div>
              <Progress value={session.total_size > 0 ? pct : 0} />
              {session.status === "completed" && (() => {
                const checksum = session.computed_sha256;
                const matches = !!checksum && (!session.sha256 || checksum.toLowerCase() === session.sha256.toLowerCase());
                const state = !checksum || !matches
                  ? "agents.files_transfer_unverified"
                  : session.sha256 ? "agents.files_transfer_verified" : "agents.files_transfer_recorded";
                return (
                  <p className="break-all font-mono text-[11px] text-muted-foreground" title={checksum || undefined}>
                    {checksum ? t("agents.files_transfer_checksum", { hash: `${checksum.slice(0, 12)}…` }) : t(state)}
                    {checksum ? ` · ${t(state)}` : ""}
                  </p>
                );
              })()}
              {session.error && <p className="text-xs text-destructive-ink">{session.error}</p>}
              <div className="flex flex-wrap gap-1">
                {session.status === "active" && (
                  <>
                    <Permission perms="files.write">
                      <Button variant="ghost" size="sm" disabled={busy} onClick={() => onPause(session.id)}>
                        <Pause className="size-3.5" /> {t("agents.files_transfer_pause")}
                      </Button>
                    </Permission>
                    <Permission perms="files.write">
                      <Button variant="ghost" size="sm" disabled={busy} onClick={() => onCancel(session.id)}>
                        <X className="size-3.5" /> {t("agents.files_transfer_cancel")}
                      </Button>
                    </Permission>
                  </>
                )}
                {session.status === "paused" && (
                  <>
                    <Permission perms={transferPerm}>
                      <Button variant="ghost" size="sm" disabled={busy} onClick={() => onResume(session)}>
                        <Play className="size-3.5" /> {t("agents.files_transfer_resume")}
                      </Button>
                    </Permission>
                    <Permission perms="files.write">
                      <Button variant="ghost" size="sm" disabled={busy} onClick={() => onCancel(session.id)}>
                        <X className="size-3.5" /> {t("agents.files_transfer_cancel")}
                      </Button>
                    </Permission>
                  </>
                )}
                {session.status === "failed" && (
                  <Permission perms={transferPerm}>
                    <Button variant="ghost" size="sm" disabled={busy} onClick={() => onStartOver(session)}>
                      <RotateCcw className="size-3.5" /> {t("agents.files_transfer_start_over")}
                    </Button>
                  </Permission>
                )}
                {session.status === "completed" && session.direction === "pull" && (
                  <Permission perms="files.read">
                    <Button variant="ghost" size="sm" disabled={busy} onClick={() => onDownload(session)}>
                      <Download className="size-3.5" /> {t("agents.files_transfer_download")}
                    </Button>
                  </Permission>
                )}
              </div>
            </div>
          );
        })}
      </Card>
    </Permission>
  );
});
