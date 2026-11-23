import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { toast } from "sonner";
import { useWS } from "@/lib/wsContext";
import { useI18n } from "@/lib/i18n";
import { paths } from "@/lib/api-paths";
import { useRefreshGate } from "@/lib/hooks/useRefreshGate";
import { normalizeCredentialData, type CredentialData } from "./types";

// One immediate refresh plus at most one more per window when a harvest burst
// emits a credential_update per row.
const CREDENTIAL_RELOAD_THROTTLE_MS = 750;

export function useCredentialsData() {
  const { t } = useI18n();
  const { subscribe } = useWS();
  const [data, setData] = useState<CredentialData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Shared in-flight promise: two rapid WS credential_update events used to
  // both pass the loadingRef guard (only synced at render commit) and start
  // overlapping loadData()s that could resolve out of order — an older
  // response overwriting fresher vault data. Callers now join the same
  // in-flight fetch instead.
  const inflightRef = useRef<Promise<void> | null>(null);
  // A WebSocket-driven refresh must not swap the populated vault for a
  // skeleton, and a failed refresh must not wipe the rows already on screen.
  const { beginRefresh, markLoaded } = useRefreshGate();

  const loadData = useCallback(async (signal?: AbortSignal) => {
    if (!signal && inflightRef.current) return inflightRef.current;
    const run = (async () => {
      if (!signal && beginRefresh()) setLoading(true);
      setError(null);
      try {
        const result = await api.get(paths.credentials.list("format=json"), { signal });
        setData(normalizeCredentialData(result as Parameters<typeof normalizeCredentialData>[0]));
        markLoaded();
      } catch (e) {
        if (signal?.aborted) return;
        const msg = e instanceof Error ? e.message : t("cred.toast.load_failed");
        setError(msg);
        toast.error(msg);
      } finally {
        if (!signal?.aborted) setLoading(false);
      }
    })();
    if (!signal) {
      // Compare against the wrapped promise, not `run`: the guard stores
      // run.finally(...), so `inflightRef.current === run` was never true and
      // the guard was never released — after the first WebSocket refresh every
      // later event joined a settled promise and the vault silently stopped
      // live-updating.
      const tracked = run.finally(() => {
        if (inflightRef.current === tracked) inflightRef.current = null;
      });
      inflightRef.current = tracked;
    }
    return run;
  }, [t, beginRefresh, markLoaded]);

  useEffect(() => {
    const controller = new AbortController();
    loadData(controller.signal);
    return () => controller.abort();
  }, [loadData]);

  // Leading + trailing throttle for WS-driven refreshes: the first event
  // reloads at once, then at most one more refresh per window, so a harvest
  // burst cannot queue N full vault fetches.
  const lastReloadAtRef = useRef(0);
  const trailingReloadRef = useRef<number | null>(null);
  const scheduleReload = useCallback(() => {
    const now = Date.now();
    const elapsed = now - lastReloadAtRef.current;
    if (elapsed >= CREDENTIAL_RELOAD_THROTTLE_MS) {
      lastReloadAtRef.current = now;
      void loadData();
      return;
    }
    if (trailingReloadRef.current !== null) return;
    trailingReloadRef.current = window.setTimeout(() => {
      trailingReloadRef.current = null;
      lastReloadAtRef.current = Date.now();
      void loadData();
    }, CREDENTIAL_RELOAD_THROTTLE_MS - elapsed);
  }, [loadData]);

  // Live vault updates: new credentials found by agents or edited by other
  // operators trigger a refresh; the sync snapshot covers WS reconnects.
  //
  // A harvesting burst emits one credential_update per row, and each used to
  // start a full 5 000-row refetch — the page reloaded the whole vault N times
  // in a few hundred milliseconds. The first event still refreshes
  // immediately; the rest of the burst coalesces into one trailing refresh.
  useEffect(() => {
    const unsub = subscribe((msg) => {
      if (msg.type === "credential_update" || msg.type === "sync") scheduleReload();
    });
    return () => {
      unsub();
      if (trailingReloadRef.current !== null) {
        clearTimeout(trailingReloadRef.current);
        trailingReloadRef.current = null;
      }
    };
  }, [subscribe, scheduleReload]);

  return { data, setData, loading, error, loadData, reload: () => loadData() };
}
