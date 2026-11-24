
import { useState } from "react";
import { paths } from "@/lib/api-paths";
import { registerFragment, useI18n } from "@/lib/i18n";
import { enCred } from "@/lib/i18n/fragments/en.cred";
registerFragment(enCred);
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { useLiveTaskResult } from "@/lib/hooks/useLiveTaskResult";
import { Spinner } from "@/components/ui/spinner";
import { Banner } from "@/components/ui/banner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import CredentialPicker from "@/pages/credentials/components/CredentialPicker";
import { Shield, Play, CheckCircle2, XCircle, AlertTriangle, Lock, Database, Key } from "lucide-react";
import { toast } from "sonner";

interface SpraySummary {
  total: number;
  valid: number;
  invalid: number;
  locked: number;
  errors: number;
}

interface SprayResultRow {
  user: string;
  status: string;
  error?: string;
}

interface SprayOutput {
  summary: SpraySummary;
  results: SprayResultRow[];
}

function parseSprayOutput(raw: string): SprayOutput | null {
  try {
    const data = JSON.parse(raw) as SprayOutput;
    if (!data || !Array.isArray(data.results)) return null;
    return data;
  } catch {
    return null;
  }
}

export default function PasswordSprayPanel({ selectedAgent }: { selectedAgent: string }) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  const [mode, setMode] = useState<"manual" | "vault">("manual");
  const [vaultCredentialId, setVaultCredentialId] = useState<number | null>(null);
  const [domain, setDomain] = useState("");
  const [dc, setDc] = useState("");
  const [delayMs, setDelayMs] = useState("500");
  const [usernames, setUsernames] = useState("");
  const [lastResult, setLastResult] = useState<{
    task_id: number;
    summary?: SpraySummary;
    results?: SprayResultRow[];
    raw?: string;
  } | null>(null);
  const live = useLiveTaskResult({ timeoutMs: 600_000 });


  // Agent selection comes from the Toolkit page header; this panel no longer
  // fetches its own agent list.
  const usernameList = usernames.split("\n").filter((u) => u.trim());
  const running = live.running;
  const canSubmit = selectedAgent && usernameList.length > 0 && !running && (
    mode === "vault" ? !!vaultCredentialId : !!password && !!domain
  );

  async function handleSpray() {
    if (!canSubmit) return;
    setLastResult(null);
    toast.success(t("spray.task_sent"));
    const body: Record<string, string> = {
      dc,
      delay_ms: delayMs,
      usernames: usernames.trim(),
    };
    if (mode === "vault" && vaultCredentialId !== null) {
      body.credential_id = String(vaultCredentialId);
    } else {
      body.password = password;
      body.domain = domain;
    }
    const final = await live.run(selectedAgent, paths.agents.cmd(selectedAgent, "password_spray"), body);
    if (!final) {
      toast.error(t("spray.send_failed") + ": " + live.error);
      return;
    }
    if (final.status === "completed") {
      const parsed = parseSprayOutput(final.result);
      if (parsed) {
        setLastResult({ task_id: final.id, summary: parsed.summary, results: parsed.results });
      } else {
        setLastResult({ task_id: final.id, raw: final.result });
      }
    } else {
      toast.error(t("spray.send_failed") + ": " + (final.error || live.error));
    }
  }

  const statusIcon = (status: string) => {
    switch (status) {
      case "valid": return <CheckCircle2 className="size-4 text-success-ink" />;
      case "locked": return <Lock className="size-4 text-warning-ink" />;
      case "error": return <AlertTriangle className="size-4 text-warning-ink" />;
      default: return <XCircle className="size-4 text-destructive-ink" />;
    }
  };

  return (
    <>
      <div className="grid gap-6 lg:grid-cols-2">
        <Card className="p-(--card-spacing) space-y-4">
          <h3 className="text-lg font-semibold flex items-center gap-2">
            <Shield className="size-5 text-primary" />
            {t("spray.config_title")}
          </h3>

          <div role="group" aria-label={t("cred.mode.label")} className="flex flex-wrap gap-2">
            <Button
              type="button"
              data-testid="spray-mode-manual"
              aria-pressed={mode === "manual"}
              variant={mode === "manual" ? "default" : "outline"}
              onClick={() => setMode("manual")}
            >
              {t("cred.mode.manual")}
            </Button>
            <Button
              type="button"
              data-testid="spray-mode-vault"
              aria-pressed={mode === "vault"}
              variant={mode === "vault" ? "default" : "outline"}
              onClick={() => setMode("vault")}
            >
              {t("cred.mode.vault")}
            </Button>
          </div>

          <div className="grid grid-cols-2 gap-4">
            {mode === "manual" && (
              <div className="space-y-1.5">
                <Label htmlFor="spray-domain">{t("spray.domain")} *</Label>
                <Input id="spray-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="CORP" />
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="spray-dc-ip">{t("spray.dc_ip")}</Label>
              <Input id="spray-dc-ip" value={dc} onChange={(e) => setDc(e.target.value)} placeholder={t("spray.dc_hint")} />
            </div>
          </div>

          {mode === "vault" ? (
            <div className="space-y-1.5">
              <CredentialPicker
                id="spray-credential-picker"
                selectedId={vaultCredentialId}
                onSelect={setVaultCredentialId}
                allowedTypes={["password", "cleartext"]}
              />
              <p className="text-xs text-muted-foreground" data-testid="spray-vault-hint">{t("spray.vault_mode_hint")}</p>
            </div>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="spray-password" className="flex items-center gap-1.5">
                <Key className="size-3.5" />
                {t("spray.password")} *
              </Label>
              <Input id="spray-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={t("spray.password_placeholder")} />
            </div>
          )}

          <div className="space-y-1.5">
            <Label htmlFor="spray-delay-ms">{t("spray.delay_ms")}</Label>
            <Input id="spray-delay-ms" type="number" value={delayMs} onChange={(e) => setDelayMs(e.target.value)} min="0" max="30000" />
          </div>

          <div className="space-y-1.5">
            <Label>{t("spray.usernames")} * <span className="text-xs text-muted-foreground">({usernameList.length} {t("spray.users_counted")})</span></Label>
            <Textarea
              value={usernames}
              onChange={(e) => setUsernames(e.target.value)}
              placeholder={t("spray.usernames_placeholder")}
              rows={10}
              className="font-mono text-sm"
            />
          </div>

          <Button onClick={handleSpray} disabled={!canSubmit} variant="gradient" className="w-full">
            <Play className="size-4" />
            {running ? t("spray.sending") : t("spray.execute")}
          </Button>
        </Card>

        <Card className="p-(--card-spacing) space-y-4">
          <h3 className="text-lg font-semibold">{t("spray.results_title")}</h3>

          {!lastResult ? (
            <div className="flex items-center justify-center gap-3 h-64 text-muted-foreground">
              {running && <Spinner className="size-5" />}
              {running ? t("spray.waiting_result") : t("spray.no_results")}
            </div>
          ) : (
            <div className="space-y-4">
              {lastResult.summary && lastResult.summary.valid > 0 && (
                <Banner tone="success" icon={<Database className="size-4" />}>
                  {t("spray.creds_imported")}
                </Banner>
              )}
              {lastResult.raw && (
                <pre className="max-h-96 overflow-y-auto text-xs font-mono bg-muted/50 rounded-lg p-3 whitespace-pre-wrap">
                  {lastResult.raw}
                </pre>
              )}
              {lastResult.summary && (
                <div className="grid grid-cols-5 gap-2">
                  {[
                    { label: t("spray.total"), value: lastResult.summary.total, variant: "outline" as const },
                    { label: t("spray.valid"), value: lastResult.summary.valid, variant: "success" as const },
                    { label: t("spray.invalid"), value: lastResult.summary.invalid, variant: "destructive" as const },
                    { label: t("spray.locked"), value: lastResult.summary.locked, variant: "warning" as const },
                    { label: t("spray.errors"), value: lastResult.summary.errors, variant: "secondary" as const },
                  ].map((s) => (
                    <div key={s.label} className="text-center">
                      <div className="text-2xl font-bold">{s.value}</div>
                      <Badge variant={s.variant} className="text-xs">{s.label}</Badge>
                    </div>
                  ))}
                </div>
              )}

              {lastResult.results && (
                <div className="max-h-96 overflow-y-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead className="text-left">{t("spray.col_status")}</TableHead>
                        <TableHead className="text-left">{t("spray.col_username")}</TableHead>
                        <TableHead className="text-left">{t("spray.col_detail")}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {lastResult.results.map((r, i) => (
                        <TableRow key={i}>
                          <TableCell>{statusIcon(r.status)}</TableCell>
                          <TableCell className="font-mono">{r.user}</TableCell>
                          <TableCell className="text-muted-foreground">{r.error || r.status}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}

              {!lastResult.summary && !lastResult.raw && lastResult.task_id && (
                <div className="flex items-center justify-center h-32 text-muted-foreground">
                  {t("spray.waiting_result")}
                </div>
              )}
            </div>
          )}
        </Card>
      </div>
    </>
  );
}
