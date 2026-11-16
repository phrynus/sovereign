import { memo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ClipboardCopy, ClipboardPaste, RefreshCw, Send } from "lucide-react";
import type { ClipboardState } from "./useWebRTCLive";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpClipboardProps {
  t: TKey;
  clipboard: ClipboardState | null;
  error: string;
  onRead: () => void;
  onCopyToLocal: () => void;
  onPasteLocal: () => void;
  onSendText: (text: string) => void;
}

/** How much of the clipboard value the strip shows before eliding. */
export const CLIPBOARD_PREVIEW_CHARS = 160;

/** One-line preview of a clipboard value: newlines collapse, long text elides. */
export function clipboardPreview(text: string): string {
  const flat = text.replace(/\s+/g, " ").trim();
  // Slice by code point, not UTF-16 unit: a plain slice can cut an emoji in
  // half and render a replacement glyph.
  const chars = Array.from(flat);
  if (chars.length <= CLIPBOARD_PREVIEW_CHARS) return flat;
  return `${chars.slice(0, CLIPBOARD_PREVIEW_CHARS).join("")}…`;
}

/**
 * Live clipboard strip: shows what the target currently holds, lets the
 * operator copy it locally, and sends either the browser clipboard or typed
 * text to the target. The manual box exists because `navigator.clipboard` is
 * unavailable on plain-HTTP deployments and in some permission states.
 */
export default memo(function RdpClipboard({
  t, clipboard, error, onRead, onCopyToLocal, onPasteLocal, onSendText,
}: RdpClipboardProps) {
  const [manual, setManual] = useState("");
  const hasText = Boolean(clipboard?.text);

  return (
    <Card className="rounded-lg px-3 py-2 shadow-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_clipboard")}</span>
        {clipboard ? (
          <Badge variant="secondary" className="text-micro-sm text-muted-foreground">
            {clipboard.source === "operator" ? t("agents.rdp_clipboard_from_operator") : t("agents.rdp_clipboard_from_target")}
          </Badge>
        ) : null}
        {clipboard?.truncated ? (
          <Badge variant="secondary" className="text-micro-sm text-warning-ink">
            {t("agents.rdp_clipboard_truncated")}
          </Badge>
        ) : null}

        <span
          className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground"
          title={hasText ? clipboardPreview(clipboard!.text) : undefined}
        >
          {hasText ? clipboardPreview(clipboard!.text) : t("agents.rdp_clipboard_empty")}
        </span>

        <Button
          variant="outline"
          size="sm"
          onClick={onRead}
          title={t("agents.rdp_clipboard_read")}
          aria-label={t("agents.rdp_clipboard_read")}
        >
          <RefreshCw className="size-3.5" />
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={onCopyToLocal}
          disabled={!hasText}
          title={t("agents.rdp_clipboard_copy")}
          aria-label={t("agents.rdp_clipboard_copy")}
        >
          <ClipboardCopy className="size-3.5" />
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={onPasteLocal}
          title={t("agents.rdp_clipboard_paste")}
          aria-label={t("agents.rdp_clipboard_paste")}
        >
          <ClipboardPaste className="size-3.5" />
        </Button>
      </div>

      <div className="mt-2 flex items-center gap-2">
        <input
          className="h-7 min-w-0 flex-1 rounded-md border border-border bg-background px-2 font-mono text-xs"
          placeholder={t("agents.rdp_clipboard_manual")}
          value={manual}
          onChange={(e) => setManual(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && manual) {
              onSendText(manual);
              setManual("");
            }
            // Keep the remote keyboard out of the way while typing here.
            e.stopPropagation();
          }}
        />
        <Button
          size="sm"
          onClick={() => {
            if (!manual) return;
            onSendText(manual);
            setManual("");
          }}
          disabled={!manual}
          title={t("agents.rdp_clipboard_send")}
        >
          <Send className="size-3.5" />
        </Button>
      </div>

      {error ? (
        <p className="mt-1 text-micro-sm text-destructive-ink" role="status">
          {error}
        </p>
      ) : null}
    </Card>
  );
});
