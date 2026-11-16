import { describe, expect, it } from "vitest";
import { DEFAULT_AUDIO_VOLUME, clampAudioVolume } from "./RdpAudio";

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
