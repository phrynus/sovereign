import { memo, useState } from "react";
import { ArrowRightLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { api, formatThrownError } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";

type TKey = (key: string, params?: Record<string, string | number>) => string;

/**
 * Transports the console offers. Mirrors the server's supportedTransportSchemes
 * (internal/server/transport_migration.go) and the agent's supportedBeaconTransport
 * (internal/payload/agent/agent_c2mode.go). Keeping this in sync avoids offering
 * a switch the next beacon would refuse.
 */
export const TRANSPORT_OPTIONS = [
  "http", "https", "tcp", "tls", "udp", "quic", "icmp", "ws", "wss",
  "grpc", "grpcs", "ssh", "mtls", "h2c", "dns", "smb",
] as const;
export type TransportOption = (typeof TRANSPORT_OPTIONS)[number];

/** Mirrors the server's transportURLFamilies; other transports use strict scheme===transport. */
const URL_FAMILIES: Partial<Record<TransportOption, string[]>> = {
  http: ["http", "https"],
  tcp: ["tcp", "tls"],
  ws: ["ws", "wss"],
  wss: ["ws", "wss"],
  grpc: ["grpc", "grpcs"],
  grpcs: ["grpc", "grpcs"],
};

export type TransportProblem = "unsupported" | "invalid" | "mismatch";

/**
 * Client-side mirror of the server's transport/URL gate. The server still
 * validates on receipt; this exists so an obvious mistake is fixed before a
 * one-shot command is consumed by the next beacon.
 */
export function validateTransportSelection(transport: string, c2Url: string): TransportProblem | null {
  if (!TRANSPORT_OPTIONS.includes(transport as TransportOption)) return "unsupported";
  const raw = c2Url.trim();
  if (!raw) return null;
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return "invalid";
  }
  const scheme = parsed.protocol.replace(":", "").toLowerCase();
  if (!scheme || !parsed.host) return "invalid";
  const allowed = URL_FAMILIES[transport as TransportOption];
  if (allowed ? !allowed.includes(scheme) : scheme !== transport) return "mismatch";
  return null;
}

interface TransportMigrateCardProps {
  agentId: string;
  online: boolean;
}

/** Operator control for the one-shot `POST /agents/:id/transport` migration. */
export default memo(function TransportMigrateCard({ agentId, online }: TransportMigrateCardProps) {
  const { t } = useI18n() as { t: TKey };
  const [transport, setTransport] = useState<TransportOption>("tcp");
  const [c2Url, setC2Url] = useState("");
  const [error, setError] = useState("");
  const [queuedTarget, setQueuedTarget] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const problem = validateTransportSelection(transport, c2Url);
    if (problem) {
      setError(t(`agents.detail_transport_err_${problem}`));
      setQueuedTarget("");
      return;
    }
    setError("");
    setQueuedTarget("");
    setBusy(true);
    try {
      const target = c2Url.trim();
      await api.postJson(paths.agents.transport(agentId), {
        transport,
        ...(target ? { c2_url: target } : {}),
      });
      setQueuedTarget(transport.toUpperCase() + (target ? " → " + target : ""));
    } catch (err) {
      setError(formatThrownError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="mb-4 flex flex-wrap items-center gap-2 px-3 py-2 shadow-sm">
      <ArrowRightLeft className="size-4 shrink-0 text-muted-foreground" />
      <span className="text-xs font-medium text-foreground">{t("agents.detail_transport_title")}</span>
      <select
        className="h-8 rounded-md border border-border bg-background px-2 text-xs"
        value={transport}
        onChange={(e) => setTransport(e.target.value as TransportOption)}
        aria-label={t("agents.detail_transport_select")}
      >
        {TRANSPORT_OPTIONS.map((option) => (
          <option key={option} value={option}>{option.toUpperCase()}</option>
        ))}
      </select>
      <Input
        className="h-8 w-64 text-xs"
        placeholder={t("agents.detail_transport_url_hint")}
        value={c2Url}
        onChange={(e) => setC2Url(e.target.value)}
        aria-label={t("agents.detail_transport_url")}
      />
      <Button size="sm" variant="outline" disabled={!online || busy} onClick={() => void submit()}>
        {busy ? t("agents.detail_transport_busy") : t("agents.detail_transport_queue")}
      </Button>
      {!online && <span className="text-xs text-muted-foreground">{t("agents.detail_transport_offline")}</span>}
      {queuedTarget && (
        <span className="text-xs text-success-ink" role="status">
          {t("agents.detail_transport_queued", { target: queuedTarget })}
        </span>
      )}
      {error && <span className="text-xs text-destructive-ink" role="status">{error}</span>}
    </Card>
  );
});
