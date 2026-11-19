import { describe, expect, it } from "vitest";
import {
  BEACON_TRANSPORTS,
  isExperimentalTransport,
  qualityLabelKey,
  transportQuality,
  visibleBeaconTransports,
} from "./transport-quality";

describe("transportQuality", () => {
  it("does not treat experimental transports as core", () => {
    expect(transportQuality("http")).toBe("core");
    expect(transportQuality("tcp")).toBe("core");
    expect(transportQuality("dns")).toBe("hardened");
    expect(transportQuality("wss")).toBe("hardened");
    // Matches docs/CAPABILITY_MATRIX.md — only mTLS/h2c are experimental.
    expect(transportQuality("grpc")).toBe("hardened");
    expect(transportQuality("ssh")).toBe("hardened");
    expect(transportQuality("icmp")).toBe("hardened");
    expect(transportQuality("udp")).toBe("hardened");
    expect(transportQuality("quic")).toBe("hardened");
    expect(transportQuality("mtls")).toBe("experimental");
    expect(transportQuality("h2c")).toBe("experimental");
    expect(isExperimentalTransport("http")).toBe(false);
    expect(isExperimentalTransport("grpc")).toBe(false);
    expect(isExperimentalTransport("mtls")).toBe(true);
  });

  it("hides experimental options until expanded", () => {
    const coreish = visibleBeaconTransports(false);
    expect(coreish.every((t) => t.quality !== "experimental")).toBe(true);
    expect(coreish.map((t) => t.value)).toEqual(["http", "tcp", "dns", "wss", "grpc", "ssh", "icmp", "udp", "quic"]);
    const all = visibleBeaconTransports(true);
    expect(all).toHaveLength(BEACON_TRANSPORTS.length);
    expect(all.some((t) => t.value === "icmp")).toBe(true);
    expect(qualityLabelKey("experimental")).toBe("generate.quality_experimental");
  });
});
