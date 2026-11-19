export type TransportQuality = "core" | "hardened" | "experimental";

interface TransportOption {
  value: string;
  label: string;
  quality: TransportQuality;
}

/** Operator-facing beacon transports with honest quality. */
export const BEACON_TRANSPORTS: TransportOption[] = [
  { value: "http", label: "HTTP(S)", quality: "core" },
  { value: "tcp", label: "TCP", quality: "core" },
  { value: "dns", label: "DNS", quality: "hardened" },
  { value: "wss", label: "WSS", quality: "hardened" },
  // Quality labels must match docs/CAPABILITY_MATRIX.md's transport table:
  // gRPC/SSH/ICMP/UDP/QUIC are Hardened (only mTLS/h2c stay Experimental).
  // check:transport-matrix enforces this in both npm task lists.
  { value: "grpc", label: "gRPC", quality: "hardened" },
  { value: "ssh", label: "SSH", quality: "hardened" },
  { value: "icmp", label: "ICMP", quality: "hardened" },
  { value: "mtls", label: "mTLS", quality: "experimental" },
  { value: "h2c", label: "H2C", quality: "experimental" },
  { value: "udp", label: "UDP", quality: "hardened" },
  { value: "quic", label: "QUIC", quality: "hardened" },
];

export function transportQuality(value: string): TransportQuality {
  return BEACON_TRANSPORTS.find((t) => t.value === value)?.quality ?? "experimental";
}

export function isExperimentalTransport(value: string): boolean {
  return transportQuality(value) === "experimental";
}

export function visibleBeaconTransports(showExperimental: boolean): TransportOption[] {
  return BEACON_TRANSPORTS.filter((t) => showExperimental || t.quality !== "experimental");
}

export function qualityLabelKey(quality: TransportQuality): string {
  return `generate.quality_${quality}`;
}
