import { memo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { BellRing, Radio } from "lucide-react";
import type { KeywordAlert } from "./useWebRTCLive";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpKeywordWatchProps {
  t: TKey;
  enabled: boolean;
  alerts: KeywordAlert[];
  onConfigure: (opts: {
    enabled: boolean;
    keywords: string[];
    minIntervalMs?: number;
    clipboard?: boolean;
    windowTitle?: boolean;
  }) => boolean;
}

/**
 * Keyword alert watcher (OverLord `notification_config`). The operator enters
 * comma-separated terms; the agent reports a bounded preview whenever new
 * clipboard text or an active-window title matches. Nothing is written back to
 * the target and full clipboard content never leaves the bounded alert preview.
 */
export default memo(function RdpKeywordWatch({ t, enabled, alerts, onConfigure }: RdpKeywordWatchProps) {
  const [terms, setTerms] = useState("");

  const apply = (next: boolean) => {
    const keywords = terms.split(/[,，\n]/).map((s) => s.trim()).filter(Boolean);
    if (next && keywords.length === 0) return;
    onConfigure({
      enabled: next,
      keywords,
      minIntervalMs: 2000,
      clipboard: true,
      windowTitle: true,
    });
  };

  return (
    <Card className="mt-3 rounded-lg px-3 py-2 shadow-sm">
      <div className="flex items-center gap-2">
        <BellRing className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_keywords")}</span>
        <Button
          variant={enabled ? "destructive" : "outline"}
          size="sm"
          onClick={() => apply(!enabled)}
          title={enabled ? t("agents.rdp_keywords_stop") : t("agents.rdp_keywords_start")}
        >
          <Radio className={`size-3.5 ${enabled ? "animate-pulse" : ""}`} />
          {enabled ? t("agents.rdp_keywords_stop") : t("agents.rdp_keywords_start")}
        </Button>
        {!enabled && <span className="text-micro-sm text-muted-foreground">{t("agents.rdp_keywords_hint")}</span>}
      </div>
      <div className="mt-2 flex items-center gap-2">
        <Input
          value={terms}
          onChange={(e) => setTerms(e.target.value)}
          placeholder={t("agents.rdp_keywords_placeholder")}
          className="h-7 text-micro-sm"
          aria-label={t("agents.rdp_keywords")}
        />
      </div>
      {alerts.length > 0 && (
        <ul className="mt-2 max-h-32 space-y-1 overflow-auto">
          {alerts.map((a, i) => (
            <li key={`${a.ts}-${i}`} className="flex gap-2 rounded-md bg-muted/40 px-2 py-1 font-mono text-micro-sm">
              <span className="shrink-0 text-warning-ink">{a.source === "window" ? "[win]" : "[clip]"}</span>
              <span className="shrink-0 text-foreground">{a.term}</span>
              <span className="truncate text-muted-foreground">{a.preview}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
});
