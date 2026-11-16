import { memo } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ShieldCheck, ShieldAlert } from "lucide-react";

type TKey = (key: string, params?: Record<string, string | number>) => string;

export interface ControlState {
  holder: string;
  you: boolean;
}

interface RdpControlProps {
  t: TKey;
  control: ControlState | null;
  onTake: () => void;
  onRelease: () => void;
}

/**
 * Exclusive-control badge. The server grants one operator at a time; this strip
 * says who holds it, lets a supervisor take over explicitly (audited), and
 * offers a release so the next operator is not locked out for the TTL.
 */
export default memo(function RdpControl({ t, control, onTake, onRelease }: RdpControlProps) {
  if (!control) {
    return (
      <div className="flex items-center gap-2 text-micro-sm text-muted-foreground">
        <ShieldCheck className="size-3.5" />
        {t("agents.rdp_control_free")}
      </div>
    );
  }
  if (control.you) {
    return (
      <div className="flex items-center gap-2 text-micro-sm text-success-ink">
        <ShieldCheck className="size-3.5" />
        <Badge variant="secondary" className="text-micro-sm">
          {t("agents.rdp_control_you")}
        </Badge>
        <Button variant="ghost" size="sm" onClick={onRelease} title={t("agents.rdp_control_release")}>
          {t("agents.rdp_control_release")}
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2 text-micro-sm text-warning-ink">
      <ShieldAlert className="size-3.5" />
      <span>{t("agents.rdp_control_other", { name: control.holder })}</span>
      <Button variant="outline" size="sm" onClick={onTake} title={t("agents.rdp_control_take")}>
        {t("agents.rdp_control_take")}
      </Button>
    </div>
  );
});
