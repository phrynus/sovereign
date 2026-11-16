import { memo } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Keyboard, Radio } from "lucide-react";

type TKey = (key: string, params?: Record<string, string | number>) => string;

/** How much of the keystroke feed the panel keeps in the DOM. */
export const KEY_FEED_MAX_CHARS = 8000;

/**
 * Appends one delta to the live keystroke feed, keeping only the tail. The
 * agent streams indefinitely; an unbounded string would grow the tab's memory
 * for as long as the session stays open.
 */
export function appendKeyFeed(previous: string, delta: string, max = KEY_FEED_MAX_CHARS): string {
  if (!delta) return previous;
  const next = previous + delta;
  if (next.length <= max) return next;
  return next.slice(next.length - max);
}

interface RdpKeysProps {
  t: TKey;
  enabled: boolean;
  feed: string;
  onToggle: (enabled: boolean) => void;
}

/** Live keystroke feed (键盘记录), the real-time form of the keylogger task. */
export default memo(function RdpKeys({ t, enabled, feed, onToggle }: RdpKeysProps) {
  return (
    <Card className="mt-3 rounded-lg px-3 py-2 shadow-sm">
      <div className="flex items-center gap-2">
        <Keyboard className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_keys")}</span>
        <Button
          variant={enabled ? "destructive" : "outline"}
          size="sm"
          onClick={() => onToggle(!enabled)}
          title={enabled ? t("agents.rdp_keys_stop") : t("agents.rdp_keys_start")}
        >
          <Radio className={`size-3.5 ${enabled ? "animate-pulse" : ""}`} />
          {enabled ? t("agents.rdp_keys_stop") : t("agents.rdp_keys_start")}
        </Button>
        {!enabled && <span className="text-micro-sm text-muted-foreground">{t("agents.rdp_keys_hint")}</span>}
      </div>
      <pre
        className="mt-2 max-h-40 min-h-10 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted/40 p-2 font-mono text-micro-sm text-foreground"
        aria-live="polite"
      >
        {feed || (enabled ? t("agents.rdp_keys_waiting") : t("agents.rdp_keys_empty"))}
      </pre>
    </Card>
  );
});
