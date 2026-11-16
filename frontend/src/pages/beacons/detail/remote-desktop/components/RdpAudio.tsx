import { memo, type RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Mic, MicOff, RefreshCw, Volume2, VolumeX } from "lucide-react";
import type { AudioDeviceInfo } from "./useWebRTCLive";

type TKey = (key: string, params?: Record<string, string | number>) => string;

export const DEFAULT_AUDIO_VOLUME = 0.7;

/** Clamps a slider/volume value into 0..1 (NaN becomes the default). */
export function clampAudioVolume(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_AUDIO_VOLUME;
  return Math.min(1, Math.max(0, value));
}

/**
 * Picks the device index to send after switching audio mode. The candidate
 * list is per mode, so an index from the previous mode is usually invalid;
 * keeping the current index when it still exists avoids resetting a deliberate
 * choice, otherwise the first device of the new mode is used.
 */
export function nextDeviceForMode(devices: AudioDeviceInfo[], current: number): number {
  const indices = devices.map((device) => device.index);
  return indices.includes(current) ? current : (indices[0] ?? 0);
}

interface RdpAudioProps {
  t: TKey;
  enabled: boolean;
  muted: boolean;
  volume: number;
  error: string;
  mode: "mic" | "speaker";
  device: number;
  /** The capture source the agent resolved (dshow device / PulseAudio source). */
  source: string;
  /** Devices the agent enumerated for the current mode (names + indices). */
  devices: AudioDeviceInfo[];
  audioRef: RefObject<HTMLAudioElement | null>;
  onToggle: (enabled: boolean) => void;
  onSource: (mode: "mic" | "speaker", device: number) => void;
  onRefresh: (mode: "mic" | "speaker") => void;
  onMute: (muted: boolean) => void;
  onVolume: (volume: number) => void;
}

/**
 * Live audio strip: the target's capture can be started/stopped, and the
 * browser element starts muted (autoplay policy), so the operator explicitly
 * unmutes. Volume is clamped before it reaches the element.
 */
export default memo(function RdpAudio({
  t, enabled, muted, volume, error, mode, device, source, devices, audioRef, onToggle, onSource, onRefresh, onMute, onVolume,
}: RdpAudioProps) {
  return (
    <Card className="mt-3 rounded-lg px-3 py-2 shadow-sm">
      <div className="flex flex-wrap items-center gap-2">
        {enabled ? <Mic className="size-3.5 text-success-ink" /> : <MicOff className="size-3.5 text-muted-foreground" />}
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_audio")}</span>
        <select
          className="h-7 rounded-md border border-border bg-background px-1.5 text-xs"
          value={mode}
          onChange={(e) => {
            const nextMode = e.target.value === "speaker" ? "speaker" : "mic";
            // The candidate list is per mode; when the current index does not
            // exist in the new mode, the first available device is selected so
            // the operator never points at a missing index.
            onSource(nextMode, nextDeviceForMode(devices, device));
          }}
          aria-label={t("agents.rdp_audio_source")}
          title={t("agents.rdp_audio_source")}
        >
          <option value="mic">{t("agents.rdp_audio_mic")}</option>
          <option value="speaker">{t("agents.rdp_audio_speaker")}</option>
        </select>
        {devices.length > 0 ? (
          <select
            className="h-7 max-w-56 rounded-md border border-border bg-background px-1.5 text-xs"
            value={device}
            onChange={(e) => onSource(mode, Number(e.target.value))}
            aria-label={t("agents.rdp_audio_device")}
            title={t("agents.rdp_audio_device")}
          >
            {devices.map((d) => (
              <option key={d.index} value={d.index}>
                {d.name}
              </option>
            ))}
          </select>
        ) : (
          <input
            type="number"
            min={0}
            max={15}
            value={device}
            onChange={(e) => {
              const next = Number(e.target.value);
              if (Number.isInteger(next) && next >= 0 && next <= 15) onSource(mode, next);
            }}
            className="h-7 w-14 rounded-md border border-border bg-background px-1.5 text-xs"
            aria-label={t("agents.rdp_audio_device")}
            title={t("agents.rdp_audio_device")}
          />
        )}
        <Button
          variant="ghost"
          size="sm"
          onClick={() => onRefresh(mode)}
          title={t("agents.rdp_audio_refresh")}
        >
          <RefreshCw className="size-3.5" />
        </Button>
        <Button
          variant={enabled ? "destructive" : "outline"}
          size="sm"
          onClick={() => onToggle(!enabled)}
          title={enabled ? t("agents.rdp_audio_stop") : t("agents.rdp_audio_start")}
        >
          {enabled ? t("agents.rdp_audio_stop") : t("agents.rdp_audio_start")}
        </Button>
        {enabled && (
          <>
            <Button
              variant="outline"
              size="sm"
              onClick={() => onMute(!muted)}
              title={muted ? t("agents.rdp_audio_unmute") : t("agents.rdp_audio_mute")}
              aria-pressed={muted}
            >
              {muted ? <VolumeX className="size-3.5" /> : <Volume2 className="size-3.5" />}
            </Button>
            <input
              type="range"
              min={0}
              max={100}
              value={Math.round(clampAudioVolume(volume) * 100)}
              onChange={(e) => onVolume(clampAudioVolume(Number(e.target.value) / 100))}
              className="w-28"
              aria-label={t("agents.rdp_audio_volume")}
              title={t("agents.rdp_audio_volume")}
            />
          </>
        )}
        {enabled && source && (
          <span className="max-w-56 truncate font-mono text-micro-sm text-muted-foreground" title={source}>
            {source}
          </span>
        )}
        {!enabled && <span className="text-micro-sm text-muted-foreground">{t("agents.rdp_audio_hint")}</span>}
        {error ? (
          <span className="text-micro-sm text-destructive-ink" role="status">
            {error}
          </span>
        ) : null}
      </div>
      <audio ref={audioRef} autoPlay muted={muted} className="hidden" />
    </Card>
  );
});
