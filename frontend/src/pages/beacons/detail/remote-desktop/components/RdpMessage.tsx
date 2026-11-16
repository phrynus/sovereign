import { memo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { MessageSquare, Send } from "lucide-react";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpMessageProps {
  t: TKey;
  status: "shown" | "failed" | "";
  onSend: (text: string) => boolean;
}

/**
 * Remote chat (远程交谈): shows a dialog on the target and reports whether it
 * was delivered. The status matters — an operator needs to know that the person
 * in front of the screen actually saw the message.
 */
export default memo(function RdpMessage({ t, status, onSend }: RdpMessageProps) {
  const [text, setText] = useState("");

  const submit = () => {
    if (!text.trim()) return;
    if (onSend(text)) setText("");
  };

  return (
    <Card className="mt-3 rounded-lg px-3 py-2 shadow-sm">
      <div className="flex items-center gap-2">
        <MessageSquare className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_message")}</span>
        <input
          className="h-7 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-xs"
          placeholder={t("agents.rdp_message_hint")}
          value={text}
          maxLength={1024}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
            e.stopPropagation();
          }}
        />
        <Button size="sm" onClick={submit} disabled={!text.trim()} title={t("agents.rdp_message_send")}>
          <Send className="size-3.5" />
        </Button>
        {status === "shown" && (
          <span className="text-micro-sm text-success-ink" role="status">
            {t("agents.rdp_message_shown")}
          </span>
        )}
        {status === "failed" && (
          <span className="text-micro-sm text-destructive-ink" role="status">
            {t("agents.rdp_message_failed")}
          </span>
        )}
      </div>
    </Card>
  );
});
