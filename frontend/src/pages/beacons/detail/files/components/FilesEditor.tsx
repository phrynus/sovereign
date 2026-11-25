import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { Permission } from "@/components/ui/permission";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { formatTimestamp } from "@/lib/agent-files/types";
import { useFileEditor } from "./useFileEditor";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface FilesEditorProps {
  t: TKey;
  agentId: string;
  /** Full remote path used by the read/write tasks. */
  path: string;
  filename: string;
  onClose: () => void;
  onSaved?: () => void;
}

export default function FilesEditor(props: FilesEditorProps) {
  return <Permission perms="files.read"><FilesEditorContent {...props} /></Permission>;
}

function FilesEditorContent({ t, agentId, path, filename, onClose, onSaved }: FilesEditorProps) {
  const editor = useFileEditor({ agentId, path, t });
  const { confirm, modal } = useConfirm();
  const [showHistory, setShowHistory] = useState(false);
  const open = editor.open;

  useEffect(() => {
    void open().catch(() => { /* error is surfaced in the dialog */ });
  }, [open]);

  const handleSave = async () => {
    try {
      await editor.save();
      toast.success(t("agents.files_editor_saved", { filename }));
      onSaved?.();
    } catch {
      // Conflict and error state live in the hook; keep the draft for retry.
    }
  };

  const handleOverwrite = async () => {
    if (!(await confirm({ message: t("agents.files_editor_confirm_overwrite") }))) return;
    try {
      await editor.overwrite();
      toast.success(t("agents.files_editor_saved", { filename }));
      onSaved?.();
    } catch {
      // A fresh conflict after rebase is shown by the hook.
    }
  };

  return (
    <>
      <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
        <DialogContent size="2xl" className="flex max-h-[85vh] flex-col">
          <DialogHeader>
            <DialogTitle>{t("agents.files_editor_title", { filename })}</DialogTitle>
          </DialogHeader>
          {editor.loading && (
            <p className="text-xs text-muted-foreground">{t("agents.files_editor_loading")}</p>
          )}
          {editor.error && <p role="alert" className="text-xs text-destructive-ink">{editor.error}</p>}
          {editor.conflict && (
            <div role="alert" className="space-y-2 rounded-lg border border-destructive/50 bg-destructive/5 p-3">
              <p className="text-xs text-destructive-ink">{t("agents.files_editor_conflict")}</p>
              <div className="flex gap-2">
                <Button variant="secondary" size="sm" onClick={() => { void editor.reload().catch(() => {}); }}>
                  {t("agents.files_editor_reload")}
                </Button>
                <Permission perms="files.write">
                  <Button variant="destructive" size="sm" onClick={() => { void handleOverwrite(); }}>
                    {t("agents.files_editor_overwrite")}
                  </Button>
                </Permission>
              </div>
            </div>
          )}
          <Permission perms="files.write">
            {canWrite => (
          <Textarea
            value={editor.draft}
            onChange={(e) => editor.changeDraft(e.target.value)}
            aria-label={t("agents.files_edit")}
            className="min-h-64 flex-1 font-mono text-xs"
            disabled={editor.loading || editor.saving || !editor.baselineTaskId || !canWrite}
          />
            )}
          </Permission>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                void editor.loadHistory().catch(() => { /* history is advisory */ });
                setShowHistory(true);
              }}
            >
              {t("agents.files_editor_history")}
            </Button>
            <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
            <Permission perms="files.write">
              <Button
                disabled={!editor.dirty || editor.saving || editor.loading || editor.baselineTaskId === 0}
                onClick={() => { void handleSave(); }}
              >
                {editor.saving ? t("agents.files_editor_saving") : t("agents.files_editor_save")}
              </Button>
            </Permission>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={showHistory} onOpenChange={setShowHistory}>
        <SheetContent side="right" className="flex flex-col overflow-auto">
          <SheetHeader>
            <SheetTitle>{t("agents.files_editor_history")}</SheetTitle>
          </SheetHeader>
          {editor.revisions.length === 0 ? (
            <p className="px-4 py-6 text-center text-xs text-muted-foreground">
              {t("agents.files_editor_history_empty")}
            </p>
          ) : (
            <ul className="space-y-2 px-4 py-2">
              {editor.revisions.map((revision) => (
                <li key={revision.task_id} className="rounded-lg border border-border/60 p-2 text-xs">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono">#{revision.task_id} {revision.type}</span>
                    <span className="text-muted-foreground">{revision.operator}</span>
                    <span className="text-muted-foreground">{formatTimestamp(revision.created_at)}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => { void editor.viewRevision(revision.task_id).catch(() => {}); }}
                    >
                      {t("agents.files_editor_view")}
                    </Button>
                  </div>
                  <p className="truncate font-mono text-muted-foreground">{revision.sha256}</p>
                </li>
              ))}
            </ul>
          )}
          {editor.revisionText !== null && (
            <pre className="mx-4 mb-4 min-h-24 flex-1 overflow-auto whitespace-pre-wrap rounded-lg bg-muted/50 p-3 font-mono text-xs">
              {editor.revisionText || t("agents.files_editor_empty_snapshot")}
            </pre>
          )}
        </SheetContent>
      </Sheet>
      {modal}
    </>
  );
}
