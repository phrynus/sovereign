import { describe, expect, it } from "vitest";
import { TILE_IMAGE_PREFIX, tileFrameBytes, tileSource } from "./RegionCanvas";
import type { TileFrame } from "./useWebRTCLive";

function frameWith(payloads: string[]): TileFrame {
  return {
    width: 1280,
    height: 720,
    tile: 64,
    full: false,
    tiles: payloads.map((data, i) => ({ x: i * 64, y: 0, w: 64, h: 64, data })),
  };
}

describe("tileSource", () => {
  it("wraps a tile payload in the JPEG data URL prefix", () => {
    expect(tileSource("AAAA")).toBe(`${TILE_IMAGE_PREFIX}AAAA`);
  });
});

describe("tileFrameBytes", () => {
  it("decodes base64 to the bytes the frame actually costs", () => {
    // "AAAA" is 3 bytes; "AAAAAA==" is 4 bytes (2 padding chars).
    expect(tileFrameBytes(frameWith(["AAAA", "AAAAAA=="]))).toBe(7);
  });

  it("returns 0 for an empty or absent frame", () => {
    expect(tileFrameBytes(null)).toBe(0);
    expect(tileFrameBytes(frameWith([]))).toBe(0);
  });

  it("sums every tile, matching a fresh full-frame payload", () => {
    const small = frameWith(["A".repeat(4000), "B".repeat(4000)]);
    expect(tileFrameBytes(small)).toBe(6000);
  });
});
