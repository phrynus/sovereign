import { memo } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { WorkspaceToolbar } from "@/components/ui/workspace-toolbar";
import { Compass, Expand, Gauge, LayoutGrid, Maximize2, Monitor, Play, RefreshCw, Square, Users } from "lucide-react";
import { isExperimentalDesktop } from "../../components/session-quality";
import type { ResolutionOption } from "./useRemoteDesktop";
import type { DisplayInfo, LivePreset, VideoDeviceInfo, VideoSource } from "./useWebRTCLive";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpToolbarProps {
  t: TKey;
  agentId: string;
  nativeWidth: number;
  nativeHeight: number;
  resolutions: ResolutionOption[];
  resolution: string;
  setResolution: (v: string) => void;
  mode: "snapshot" | "live";
  setMode: (mode: "snapshot" | "live") => void;
  livePresets: Array<LivePreset & { label: string }>;
  videoSource: VideoSource;
  onSelectVideoSource: (source: VideoSource) => void;
  videoDevices: VideoDeviceInfo[];
  webcamDevice: number;
  onSelectWebcamDevice: (index: number) => void;
  onRefreshVideoDevices: (force?: boolean) => void;
  livePreset: LivePreset["value"];
  setLivePreset: (preset: LivePreset["value"]) => void;
  displays: DisplayInfo[];
  selectedDisplay: number;
  onSelectDisplay: (index: number) => void;
  onRefreshDisplays: (force?: boolean) => void;
  differential: boolean;
  onToggleDifferential: (enabled: boolean) => void;
  regionOn: boolean;
  onToggleRegion: (enabled: boolean) => void;
  active: boolean;
  isFullscreen: boolean;
  onToggleFullscreen: () => void;
  versionBlocked: boolean;
  onStart: () => void;
  onStop: () => void;
}

/** Remote-desktop header: title, transport, resolution, fullscreen, start/stop. */
export default memo(function RdpToolbar({
  t, agentId, nativeWidth, nativeHeight, resolutions, resolution, setResolution,
  mode, setMode, livePresets, livePreset, setLivePreset, videoSource, onSelectVideoSource,
  videoDevices, webcamDevice, onSelectWebcamDevice, onRefreshVideoDevices,
  displays, selectedDisplay, onSelectDisplay, onRefreshDisplays,
  differential, onToggleDifferential, regionOn, onToggleRegion,
  active, isFullscreen, onToggleFullscreen, versionBlocked, onStart, onStop,
}: RdpToolbarProps) {
  return (
    <WorkspaceToolbar
      title={t("nav.beacon_rdp")}
      icon={Users}
      badges={
        <>
          {isExperimentalDesktop("remote-desktop") ? (
            <span className="text-micro font-normal text-warning-ink">{t("generate.quality_experimental")}</span>
          ) : null}
          <Badge variant="secondary" className="text-xs text-muted-foreground font-mono">
            {agentId}
          </Badge>
          {nativeWidth > 0 && nativeHeight > 0 && (
            <Badge variant="secondary" className="text-xs bg-muted/50 text-muted-foreground flex items-center gap-1">
              <Maximize2 className="size-3.5" />
              {nativeWidth} x {nativeHeight}
            </Badge>
          )}
        </>
      }
      actions={
        <>
          <Select
            value={mode}
            onValueChange={(v) => { if (v === "snapshot" || v === "live") setMode(v); }}
            disabled={active}
          >
            <SelectTrigger className="w-[150px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="snapshot">{t("agents.rdp_mode_snapshot")}</SelectItem>
              <SelectItem value="live">{t("agents.rdp_mode_live")}</SelectItem>
            </SelectContent>
          </Select>
          {mode === "snapshot" ? (
            <Select value={resolution} onValueChange={(v) => { if (v !== null) setResolution(v); }} disabled={active}>
              <SelectTrigger className="w-[180px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {resolutions.map((r) => (
                  <SelectItem key={r.value} value={r.value}>
                    {r.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Select
              value={livePreset}
              onValueChange={(v) => {
                if (v === "720p" || v === "1080p") setLivePreset(v);
              }}
              disabled={active}
            >
              <SelectTrigger className="w-[180px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {livePresets.map((preset) => (
                  <SelectItem key={preset.value} value={preset.value}>
                    {preset.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {mode === "live" && (
            <Select
              value={videoSource}
              onValueChange={(v) => onSelectVideoSource(v === "webcam" ? "webcam" : "screen")}
              disabled={active}
            >
              <SelectTrigger className="w-[150px]" aria-label={t("agents.rdp_source")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="screen">{t("agents.rdp_source_screen")}</SelectItem>
                <SelectItem value="webcam">{t("agents.rdp_source_webcam")}</SelectItem>
              </SelectContent>
            </Select>
          )}
          {mode === "live" && videoSource === "webcam" && (
            <>
              <Select
                value={String(webcamDevice)}
                onOpenChange={(open) => { if (open) onRefreshVideoDevices(); }}
                onValueChange={(v) => {
                  const index = Number(v);
                  if (Number.isInteger(index) && index >= 0) onSelectWebcamDevice(index);
                }}
                disabled={active}
              >
                <SelectTrigger className="w-[190px]" aria-label={t("agents.rdp_camera_device")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {videoDevices.length === 0 ? (
                    <SelectItem value="0">{t("agents.rdp_camera_default")}</SelectItem>
                  ) : (
                    videoDevices.map((device) => (
                      <SelectItem key={device.index} value={String(device.index)}>
                        {device.name}
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => onRefreshVideoDevices(true)}
                disabled={active}
                title={t("agents.rdp_camera_refresh")}
              >
                <RefreshCw className="size-3.5" />
              </Button>
            </>
          )}
          {mode === "live" && videoSource === "screen" && (
            <Select
              value={String(selectedDisplay)}
              onOpenChange={(open) => { if (open) onRefreshDisplays(); }}
              onValueChange={(v) => {
                const index = Number(v);
                if (Number.isInteger(index) && index >= 0) onSelectDisplay(index);
              }}
            >
              <SelectTrigger
                className="w-[190px]"
                aria-label={t("agents.rdp_display")}
                title={t("agents.rdp_display_hint")}
              >
                <span className="flex items-center gap-1.5 truncate">
                  <Monitor className="size-3.5 shrink-0" />
                  <SelectValue />
                </span>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="0">{t("agents.rdp_display_all")}</SelectItem>
                {displays.map((display) => (
                  <SelectItem key={display.index} value={String(display.index)}>
                    {t("agents.rdp_display_n", { n: display.index })} · {display.width}x{display.height}
                    {display.primary ? ` (${t("agents.rdp_display_primary")})` : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {mode === "live" && (
            <Button
              variant={differential ? "default" : "outline"}
              size="sm"
              onClick={() => onToggleDifferential(!differential)}
              title={t("agents.rdp_differential_hint")}
              aria-pressed={differential}
              disabled={regionOn}
            >
              <Gauge className="size-3.5" />
              {t("agents.rdp_differential")}
            </Button>
          )}
          {mode === "live" && videoSource === "screen" && (
            <Button
              variant={regionOn ? "default" : "outline"}
              size="sm"
              onClick={() => onToggleRegion(!regionOn)}
              title={t("agents.rdp_region_hint")}
              aria-pressed={regionOn}
            >
              <LayoutGrid className="size-3.5" />
              {t("agents.rdp_region")}
            </Button>
          )}
          <Tooltip>
            <TooltipTrigger render={<Button
                variant="outline"
                size="icon-lg"
                onClick={onToggleFullscreen}
                aria-label={isFullscreen ? t("agents.rdp_exit_fullscreen") : t("agents.rdp_enter_fullscreen")}
              />}>
              {isFullscreen ? <Compass className="size-4" /> : <Expand className="size-4" />}
            </TooltipTrigger>
            <TooltipContent>{isFullscreen ? t("agents.rdp_exit_fullscreen") : t("agents.rdp_fullscreen")}</TooltipContent>
          </Tooltip>
          {!active ? (
            <Button
              onClick={onStart}
              disabled={versionBlocked}
              title={versionBlocked ? t("agents.version_unknown_dest") : undefined}
            >
              <Play className="size-4" />
              {t("agents.rdp_start")}
            </Button>
          ) : (
            <Button variant="destructive" onClick={onStop}>
              <Square className="size-4" />
              {t("agents.rdp_stop")}
            </Button>
          )}
        </>
      }
    />
  );
});
