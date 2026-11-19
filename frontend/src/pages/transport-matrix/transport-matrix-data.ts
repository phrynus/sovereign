/**
 * Static transport matrix reference data, derived from docs/CAPABILITY_MATRIX.md
 * (the published source of truth). Quality values come from transport-quality.ts
 * so the two cannot disagree; check:transport-matrix enforces that at build time.
 *
 * This is a reference view, not live state — per-listener health lives in
 * Circuit Breaker and migration in the agent detail card. Keeping the matrix
 * static means no new API endpoint and no first-paint bundle cost.
 */
import { BEACON_TRANSPORTS, type TransportQuality } from "@/pages/payload-generator/components/transport-quality";

export interface TransportMatrixRow {
  value: string;
  label: string;
  quality: TransportQuality;
  /** i18n key suffixes for the capability notes (en/zh fragments). */
  noteKeys: string[];
}

// Note keys map to transport.note_<key> in en/zh fragments. Several transports
// share a note family; the matrix renders all keys listed here.
const NOTES: Record<string, string[]> = {
  http: ["http"],
  tcp: ["tcp"],
  dns: ["dns"],
  wss: ["wss"],
  grpc: ["grpc"],
  ssh: ["ssh"],
  icmp: ["icmp"],
  udp: ["udp"],
  quic: ["quic"],
  mtls: ["mtls"],
  h2c: ["h2c"],
};

export const TRANSPORT_MATRIX_ROWS: TransportMatrixRow[] = BEACON_TRANSPORTS.map((t) => ({
  value: t.value,
  label: t.label,
  quality: t.quality,
  noteKeys: NOTES[t.value] ?? [],
}));
