import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * Public projection of a vault entry. The options endpoint never returns a
 * password, hash or resolved command — only enough metadata to pick a row.
 */
export interface CredentialOption {
  id: number;
  label: string;
  domain: string;
  username: string;
  type: string;
  subtype: string;
  expiry_state: "unknown" | "expired" | "expiring" | "valid";
  expires_at: string | null;
}

const EXPIRY_VARIANT: Record<CredentialOption["expiry_state"], "outline" | "destructive" | "warning" | "success"> = {
  // A 30-day review horizon is NOT an expiry. Never style unknown as expired.
  unknown: "outline",
  expired: "destructive",
  expiring: "warning",
  valid: "success",
};

/** Operator-facing summary; deliberately contains no secret material. */
export function credentialOptionSummary(option: CredentialOption): string {
  const parts = [option.label || option.username || `#${option.id}`];
  if (option.domain && !String(option.label || "").includes(option.domain)) parts.push(option.domain);
  const kind = [option.type, option.subtype].filter(Boolean).join("/");
  if (kind) parts.push(kind);
  parts.push(option.expiry_state || "unknown");
  return parts.join(" · ");
}

interface CredentialPickerProps {
  selectedId: number | null;
  onSelect: (id: number | null) => void;
  /** Optional allow-list of credential types (case-insensitive). */
  allowedTypes?: string[];
  id?: string;
  label?: string;
  className?: string;
  limit?: number;
}

/**
 * Vault credential selector. Fetching happens when the operator opens the
 * picker (focus/click) or types a debounced search term; the response is the
 * redacted `/api/credentials/options` projection, never the vault list with
 * plaintext secrets.
 */
export default function CredentialPicker({
  selectedId,
  onSelect,
  allowedTypes,
  id = "credential-picker",
  label,
  className,
  limit = 100,
}: CredentialPickerProps) {
  const { t } = useI18n();
  const [options, setOptions] = useState<CredentialOption[]>([]);
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [opened, setOpened] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seqRef = useRef(0);

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedQuery(query.trim()), 200);
    return () => clearTimeout(timer);
  }, [query]);

  const load = useCallback(async (q: string) => {
    const seq = ++seqRef.current;
    setLoading(true);
    const params = new URLSearchParams({ q, limit: String(limit) });
    try {
      const data = await api.get<{ options?: CredentialOption[] }>(
        `${paths.credentials.options}?${params.toString()}`,
      );
      if (seq !== seqRef.current) return;
      setOptions(Array.isArray(data?.options) ? data.options : []);
      setError(null);
    } catch {
      if (seq !== seqRef.current) return;
      setOptions([]);
      setError(t("cred.picker.load_failed"));
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  }, [limit, t]);

  useEffect(() => {
    if (!opened) return;
    void load(debouncedQuery);
  }, [opened, debouncedQuery, load]);

  const visibleOptions = useMemo(() => {
    if (!allowedTypes?.length) return options;
    const allowed = new Set(allowedTypes.map((type) => type.toLowerCase()));
    return options.filter((option) =>
      allowed.has(String(option.type || "").toLowerCase()) ||
      allowed.has(String(option.subtype || "").toLowerCase()),
    );
  }, [options, allowedTypes]);

  const selected = options.find((option) => option.id === selectedId) ?? null;
  const expiryState = selected?.expiry_state ?? "unknown";

  return (
    <div className={className} data-testid="credential-picker">
      <div className="space-y-1.5">
        <Label htmlFor={`${id}-search`}>{label ?? t("cred.picker.label")}</Label>
        <Input
          id={`${id}-search`}
          type="search"
          value={query}
          data-testid="credential-picker-search"
          placeholder={t("cred.picker.search")}
          aria-label={t("cred.picker.search")}
          onFocus={() => setOpened(true)}
          onChange={(event) => {
            setOpened(true);
            setQuery(event.target.value);
          }}
        />
      </div>
      <div className="mt-1.5 space-y-1.5">
        <Label htmlFor={id}>{t("cred.picker.select_label")}</Label>
        <select
          id={id}
          data-testid="credential-picker-select"
          aria-label={label ?? t("cred.picker.label")}
          className="w-full rounded-md border border-input bg-background px-2 py-1.5 text-sm"
          value={selectedId ?? ""}
          onFocus={() => setOpened(true)}
          onMouseDown={() => setOpened(true)}
          onClick={() => setOpened(true)}
          onChange={(event) => onSelect(event.target.value ? Number(event.target.value) : null)}
        >
          <option value="">{t("cred.picker.placeholder")}</option>
          {visibleOptions.map((option) => (
            <option
              key={option.id}
              value={option.id}
              data-testid={`credential-picker-option-${option.id}`}
            >
              {credentialOptionSummary(option)}
            </option>
          ))}
        </select>
      </div>
      {loading && <p className="mt-1 text-xs text-muted-foreground" data-testid="credential-picker-loading">{t("common.loading")}</p>}
      {error && <p role="alert" className="mt-1 text-xs text-destructive-ink">{error}</p>}
      {!loading && !error && opened && options.length === 0 && (
        <p className="mt-1 text-xs text-muted-foreground">{t("cred.picker.no_options")}</p>
      )}
      {selected && (
        <>
          <p className="mt-1 text-xs text-muted-foreground" data-testid="credential-picker-details">
            {selected.domain || "-"} · {selected.username || "-"} · {[selected.type, selected.subtype].filter(Boolean).join("/") || "-"}
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2" data-testid="credential-picker-selected">
          <Badge variant={EXPIRY_VARIANT[expiryState]} data-testid="credential-picker-expiry" data-expiry-state={expiryState}>
            {t(`cred.expiry_${expiryState}`)}
          </Badge>
          <span className="text-xs text-muted-foreground" data-testid="credential-picker-secret-hint">
            {t("cred.picker.secret_hint")}
          </span>
        </div>
        </>
      )}
    </div>
  );
}
