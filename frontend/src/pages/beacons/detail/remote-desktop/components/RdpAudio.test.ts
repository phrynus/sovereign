import { describe, expect, it } from "vitest";
import { DEFAULT_AUDIO_VOLUME, clampAudioVolume, nextDeviceForMode } from "./RdpAudio";

describe("clampAudioVolume", () => {
  it("keeps a valid value", () => {
    expect(clampAudioVolume(0)).toBe(0);
    expect(clampAudioVolume(0.42)).toBe(0.42);
    expect(clampAudioVolume(1)).toBe(1);
  });

  it("clamps out-of-range and non-finite values", () => {
    expect(clampAudioVolume(-0.3)).toBe(0);
    expect(clampAudioVolume(4)).toBe(1);
    expect(clampAudioVolume(Number.NaN)).toBe(DEFAULT_AUDIO_VOLUME);
    expect(clampAudioVolume(Number.POSITIVE_INFINITY)).toBe(DEFAULT_AUDIO_VOLUME);
  });
});

describe("nextDeviceForMode", () => {
  const devices = [
    { index: 0, name: "MacBook Pro Microphone", loopback: false },
    { index: 2, name: "Aggregate Device", loopback: false },
  ];

  it("keeps an index that exists in the new mode's list", () => {
    expect(nextDeviceForMode(devices, 2)).toBe(2);
  });

  it("falls back to the first device of the new mode", () => {
    expect(nextDeviceForMode(devices, 1)).toBe(0);
    expect(nextDeviceForMode([{ index: 1, name: "BlackHole 2ch", loopback: true }], 0)).toBe(1);
  });

  it("uses 0 when the target reported no devices", () => {
    expect(nextDeviceForMode([], 3)).toBe(0);
  });
});
