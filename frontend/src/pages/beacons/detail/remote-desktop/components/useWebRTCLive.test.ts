import { describe, expect, it } from "vitest";
import { DEVICE_LIST_TTL_MS, LIVE_PRESETS, buildOperatorWsUrl, deviceListStale, getLivePreset, parseDisplayState, parseTileFrame, sourceAllowsInput } from "./useWebRTCLive";

describe("WebRTC live helpers", () => {
  it("builds the authenticated operator signaling URL for both schemes", () => {
    expect(
      buildOperatorWsUrl("session abc", { protocol: "https:", host: "c2.example:8443" }),
    ).toBe("wss://c2.example:8443/ws/rd/operator?session=session%20abc");
    expect(
      buildOperatorWsUrl("s1", { protocol: "http:", host: "127.0.0.1:8000" }),
    ).toBe("ws://127.0.0.1:8000/ws/rd/operator?session=s1");
  });

  it("offers 720p30 as the baseline and 1080p30 as the ceiling", () => {
    expect(LIVE_PRESETS.map((preset) => preset.value)).toEqual(["720p", "1080p"]);
    expect(LIVE_PRESETS[0]).toMatchObject({ width: 1280, height: 720, fps: 30 });
    expect(LIVE_PRESETS[1]).toMatchObject({ width: 1920, height: 1080, fps: 30 });
    for (const preset of LIVE_PRESETS) {
      expect(preset.width * preset.height).toBeLessThanOrEqual(1920 * 1080);
      expect(preset.bitrateKbps).toBeGreaterThan(0);
    }
  });

  it("falls back to 720p for an unknown preset", () => {
    expect(getLivePreset("4k").value).toBe("720p");
    expect(getLivePreset("1080p").value).toBe("1080p");
  });
});

describe("sourceAllowsInput", () => {
  it("allows input only for screen capture", () => {
    expect(sourceAllowsInput("screen")).toBe(true);
    expect(sourceAllowsInput("webcam")).toBe(false);
  });
});

describe("deviceListStale", () => {
  const now = 1_000_000;

  it("treats a never-fetched list as stale", () => {
    expect(deviceListStale(0, now)).toBe(true);
  });

  it("keeps a list fetched within the TTL", () => {
    expect(deviceListStale(now - 1, now)).toBe(false);
    expect(deviceListStale(now - DEVICE_LIST_TTL_MS + 1, now)).toBe(false);
  });

  it("expires a list at the TTL boundary", () => {
    expect(deviceListStale(now - DEVICE_LIST_TTL_MS, now)).toBe(true);
    expect(deviceListStale(now - DEVICE_LIST_TTL_MS - 5_000, now)).toBe(true);
  });
});

describe("parseDisplayState", () => {
  it("reads the monitor list the agent announces", () => {
    const parsed = parseDisplayState({
      type: "state",
      state: "displays",
      selected: 2,
      displays: [
        { index: 1, x: 0, y: 0, width: 2560, height: 1440, primary: true },
        { index: 2, x: 2560, y: 0, width: 1920, height: 1080 },
      ],
    });
    expect(parsed?.selected).toBe(2);
    expect(parsed?.displays.map((d) => d.index)).toEqual([1, 2]);
    expect(parsed?.displays[0]?.primary).toBe(true);
  });

  it("ignores frames that are not a display announcement", () => {
    expect(parseDisplayState({ type: "state", state: "streaming" })).toBeNull();
    expect(parseDisplayState({ type: "state", state: "displays" })).toBeNull();
    expect(parseDisplayState({ type: "state", state: "displays", displays: [] })).toBeNull();
  });

  it("drops a monitor with no usable geometry instead of offering it", () => {
    const parsed = parseDisplayState({
      type: "state",
      state: "displays",
      displays: [
        { index: 1, x: 0, y: 0, width: 0, height: 0 },
        { index: 2, x: 0, y: 0, width: 1280, height: 720 },
      ],
    });
    expect(parsed?.displays).toHaveLength(1);
    expect(parsed?.displays[0]?.index).toBe(2);
    expect(parsed?.selected).toBe(0);
  });
});

describe("parseTileFrame", () => {
  it("keeps the geometry and payload of a change-only frame", () => {
    const frame = parseTileFrame({
      type: "tiles",
      width: 1280,
      height: 720,
      tile: 64,
      full: false,
      tiles: [
        { x: 64, y: 64, w: 64, h: 64, data: "AAAA" },
        { x: 128, y: 64, w: 64, h: 64, data: "BBBB" },
      ],
    });
    expect(frame).not.toBeNull();
    expect(frame?.width).toBe(1280);
    expect(frame?.height).toBe(720);
    expect(frame?.tile).toBe(64);
    expect(frame?.full).toBe(false);
    expect(frame?.tiles).toHaveLength(2);
    expect(frame?.tiles[1]).toMatchObject({ x: 128, y: 64, w: 64, h: 64 });
  });

  it("marks a full-frame repaint and defaults the tile size", () => {
    const frame = parseTileFrame({
      type: "tiles",
      width: 800,
      height: 600,
      full: true,
      tiles: [{ x: 0, y: 0, w: 800, h: 600, data: "AAAA" }],
    });
    expect(frame?.full).toBe(true);
    expect(frame?.tile).toBe(64);
  });

  it("drops tiles without geometry or payload rather than drawing garbage", () => {
    const frame = parseTileFrame({
      type: "tiles",
      width: 1280,
      height: 720,
      tiles: [
        { x: 0, y: 0, w: 0, h: 64, data: "AAAA" },
        { x: 0, y: 0, w: 64, h: 64, data: "" },
        { x: "no", y: 0, w: 64, h: 64, data: "AAAA" },
        { x: 64, y: 0, w: 64, h: 64, data: "BBBB" },
      ],
    });
    expect(frame?.tiles).toHaveLength(1);
    expect(frame?.tiles[0]?.x).toBe(64);
  });

  it("ignores a frame that is not tiles or has no usable tile at all", () => {
    expect(parseTileFrame({ type: "state", state: "connected" })).toBeNull();
    expect(parseTileFrame({ type: "tiles", width: 0, height: 720, tiles: [] })).toBeNull();
    expect(parseTileFrame({ type: "tiles", width: 1280, height: 720, tiles: [] })).toBeNull();
  });
});
