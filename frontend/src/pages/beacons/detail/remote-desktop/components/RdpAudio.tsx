import { memo, type RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Mic, MicOff, Volume2, VolumeX } from "lucide-react";

type TKey = (key: string, params?: Record<string, string | number>) => string;

export const DEFAULT_AUDIO_VOLUME = 0.7;

/** Clamps a slider/volume value into 0..1 (NaN becomes the default). */
export function clampAudioVolume(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_AUDIO_VOLUME;
  return Math.min(1, Math.max(0, value));
}

interface RdpAudioProps {
  t: TKey;
  enabled: boolean;
  muted: boolean;
  volume: number;
  error: string;
  audioRef: RefObject<HTMLAudioElement | null>;
  onToggle: (enabled: boolean) => void;
  onMute: (muted: boolean) => void;
  onVolume: (volume: number) => void;
}

/**
 * Live audio strip: the target's capture can be started/stopped, and the
 * browser element starts muted (autoplay policy), so the operator explicitly
 * unmutes. Volume is clamped before it reaches the element.
 */
export default memo(function RdpAudio({
  t, enabled, muted, volume, error, audioRef, onToggle, onMute, onVolume,
}: RdpAudioProps) {
  return (
    <Card className="mt-3 rounded-lg px-3 py-2 shadow-sm">
      <div className="flex flex-wrap items-center gap-2">
        {enabled ? <Mic className="size-3.5 text-success-ink" /> : <MicOff className="size-3.5 text-muted-foreground" />}
        <span className="text-xs font-medium text-foreground">{t("agents.rdp_audio")}</span>
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
