import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, formatThrownError } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { copyText } from "@/lib/clipboard";
import { useI18n } from "@/lib/i18n";
import type { RemoteInputEvent } from "./input-events";
import { appendKeyFeed } from "./RdpKeys";

/**
 * Live remote-desktop transport: the browser sends a recvonly H.264 offer over
 * the authenticated operator WebSocket, the agent answers from its single-use
 * signaling socket, and media flows directly or through the server's TURN
 * relay. Input events reuse the same socket; they never become tasks.
 *
 * Snapshot streaming in useRemoteDesktop is untouched, so a beacon without the
 * optional WebRTC module (or without ffmpeg) can fall back to JPEG frames.
 */

type LiveStatus = "idle" | "connecting" | "connected" | "error";

export interface LivePreset {
  value: "720p" | "1080p";
  width: number;
  height: number;
  bitrateKbps: number;
  fps: number;
}

export const LIVE_PRESETS: LivePreset[] = [
  { value: "720p", width: 1280, height: 720, bitrateKbps: 2500, fps: 30 },
  { value: "1080p", width: 1920, height: 1080, bitrateKbps: 4500, fps: 30 },
];

export function getLivePreset(value: string): LivePreset {
  return LIVE_PRESETS.find((preset) => preset.value === value) ?? LIVE_PRESETS[0];
}

export function buildOperatorWsUrl(
  sessionId: string,
  location: Pick<Location, "protocol" | "host"> & Partial<Pick<Location, "hostname">> = window.location,
): string {
  const path = `/ws/rd/operator?session=${encodeURIComponent(sessionId)}`;
  const envURL = import.meta.env.VITE_FORGEC2_WS_URL;
  if (envURL) return envURL + path;
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  const backendPort = import.meta.env.VITE_FORGEC2_BACKEND_PORT;
  const hostname = location.hostname || location.host.replace(/:\d+$/, "");
  const host = backendPort ? `${hostname}:${backendPort}` : location.host;
  return `${scheme}://${host}${path}`;
}

interface IceServerResponse {
  urls: string[];
  username?: string;
  credential?: string;
}

interface StartResponse {
  session_id?: string;
  ice_servers?: IceServerResponse[];
  expires_at?: string;
}

export interface DisplayInfo {
  index: number;
  x: number;
  y: number;
  width: number;
  height: number;
  primary?: boolean;
}

/** Last clipboard value seen on this session (from the target or a paste). */
export interface ClipboardState {
  text: string;
  source: "agent" | "operator";
  truncated: boolean;
  at: number;
}

/** One keyword alert reported by the agent's clipboard/window watcher. */
export interface KeywordAlert {
  source: "clipboard" | "window" | string;
  term: string;
  preview: string;
  ts: number;
}

/** One remembered clipboard value as listed by the agent (preview only). */
export interface ClipboardHistoryItem {
  index: number;
  preview: string;
  bytes: number;
  at: string;
}

/** What the live video track shows. Camera mode is view-only. */
export type VideoSource = "screen" | "webcam";

/** Audio quality tiers the agent maps to Opus bitrate/application. */
export type AudioQuality = "voice" | "standard" | "high";

/** Remote input targets a desktop, so only screen capture accepts it. */
export function sourceAllowsInput(source: VideoSource): boolean {
  return source === "screen";
}

/** How long an enumerated device list is trusted before it is re-requested. */
export const DEVICE_LIST_TTL_MS = 30_000;

/**
 * A cached device list older than the TTL is stale: the target may have gained
 * or lost a camera/microphone since it was enumerated (hotplug, virtual driver
 * installed, USB device unplugged). Callers re-request on the next interaction
 * rather than polling, so a probe only runs when the operator is about to use
 * the list.
 */
export function deviceListStale(fetchedAt: number, now: number, ttlMs = DEVICE_LIST_TTL_MS): boolean {
  if (!fetchedAt) return true;
  return now - fetchedAt >= ttlMs;
}

/** One camera, as enumerated by the agent (index is webcam_device). */
export interface VideoDeviceInfo {
  index: number;
  name: string;
}

/** One selectable capture device, as enumerated by the agent. */
export interface AudioDeviceInfo {
  index: number;
  name: string;
  loopback: boolean;
}

/** One changed 64 px block of a region-mode frame (JPEG, base64). */
export interface RegionTile {
  x: number;
  y: number;
  w: number;
  h: number;
  data: string;
}

/** A region-mode frame: only the blocks that changed since the last frame. */
export interface TileFrame {
  width: number;
  height: number;
  tile: number;
  full: boolean;
  tiles: RegionTile[];
}

/**
 * Normalizes a `tiles` signal. Tiles without geometry or payload are dropped,
 * and a frame with no usable tile is ignored rather than clearing the canvas.
 */
export function parseTileFrame(message: SignalMessage): TileFrame | null {
  if (message.type !== "tiles") return null;
  const width = Number(message.width);
  const height = Number(message.height);
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return null;
  const rawTiles = Array.isArray(message.tiles) ? message.tiles : [];
  const tiles: RegionTile[] = [];
  for (const item of rawTiles) {
    if (!item || typeof item !== "object") continue;
    const tile = item as RegionTile;
    const x = Number(tile.x);
    const y = Number(tile.y);
    const w = Number(tile.w);
    const h = Number(tile.h);
    if (![x, y, w, h].every(Number.isFinite) || w <= 0 || h <= 0) continue;
    if (typeof tile.data !== "string" || tile.data === "") continue;
    tiles.push({ x, y, w, h, data: tile.data });
  }
  if (tiles.length === 0) return null;
  return {
    width,
    height,
    tile: Number(message.tile) > 0 ? Number(message.tile) : 64,
    full: message.full === true,
    tiles,
  };
}

interface SignalMessage {
  type?: string;
  sdp?: string;
  candidate?: RTCIceCandidateInit;
  state?: string;
  reason?: string;
  error?: string;
  displays?: DisplayInfo[];
  selected?: number;
  text?: string;
  source?: string;
  truncated?: boolean;
  title?: string;
  status?: string;
  enabled?: boolean;
  holder?: string;
  you?: boolean;
  items?: unknown[];
  total?: number;
  tiles?: unknown[];
  width?: number;
  height?: number;
  tile?: number;
  full?: boolean;
  term?: string;
  preview?: string;
  ts?: number;
}

/**
 * Extracts the monitor list from the agent's `state: displays` frame. A frame
 * with no usable geometry is ignored (the picker keeps its previous value)
 * rather than rendering a monitor the agent cannot capture.
 */
export function parseDisplayState(
  message: SignalMessage,
): { displays: DisplayInfo[]; selected: number } | null {
  if (message.state !== "displays" || !Array.isArray(message.displays)) return null;
  const displays = message.displays.filter(
    (d) =>
      Number.isInteger(d?.index) &&
      d.index >= 1 &&
      Number(d.width) > 0 &&
      Number(d.height) > 0,
  );
  if (displays.length === 0) return null;
  const selected = typeof message.selected === "number" && message.selected >= 0 ? message.selected : 0;
  return { displays, selected };
}

function normalizeIceServers(servers: IceServerResponse[] | undefined): RTCIceServer[] {
  const out: RTCIceServer[] = [];
  for (const server of servers ?? []) {
    const urls = (server.urls ?? []).filter((url) => typeof url === "string" && url.length > 0);
    if (urls.length === 0) continue;
    out.push({
      urls,
      ...(server.username ? { username: server.username } : {}),
      ...(server.credential ? { credential: server.credential } : {}),
    });
  }
  return out;
}

export function useWebRTCLive(agentId: string) {
  const { t } = useI18n();
  const [active, setActive] = useState(false);
  const [status, setStatus] = useState<LiveStatus>("idle");
  const [error, setError] = useState("");
  const [liveWidth, setLiveWidth] = useState(0);
  const [liveHeight, setLiveHeight] = useState(0);
  const [livePreset, setLivePreset] = useState<LivePreset["value"]>("720p");
  // A rejected input event is not a session failure: show it briefly and keep
  // the desktop alive, otherwise one unsupported key would tear down a session.
  const [inputError, setInputError] = useState("");
  const [displays, setDisplays] = useState<DisplayInfo[]>([]);
  const [displaysAt, setDisplaysAt] = useState(0);
  // 0 = all displays (the historical behaviour); 1..N = one monitor. The agent
  // announces the list on connect, so the picker appears without a restart.
  const [selectedDisplay, setSelectedDisplay] = useState(0);
  // Live clipboard: the agent publishes the target's value on connect and on
  // change; a paste updates it and the target reflects it immediately.
  const [clipboard, setClipboard] = useState<ClipboardState | null>(null);
  const [clipboardError, setClipboardError] = useState("");
  // Bounded clipboard history (previews only; the full value is fetched on
  // demand and then lands in `clipboard`, so Copy/Paste work unchanged).
  const [clipboardHistory, setClipboardHistory] = useState<ClipboardHistoryItem[]>([]);
  // Outcome of the last operator chat message (shown on the target / failed).
  const [messageStatus, setMessageStatus] = useState<"shown" | "failed" | "">("");
  // Live keystroke feed: `keysOn` follows the agent's confirmation, `feed` is
  // the bounded tail of what the target typed.
  const [keysOn, setKeysOn] = useState(false);
  const [keysFeed, setKeysFeed] = useState("");
  // Keyword alerts: bounded list of matches reported by the agent's watcher.
  const [keywordAlerts, setKeywordAlerts] = useState<KeywordAlert[]>([]);
  const [keywordWatchOn, setKeywordWatchOn] = useState(false);
  // Exclusive control: the agent has one controller at a time. `null` means
  // nobody holds the lease yet (the next input claims it).
  const [control, setControl] = useState<{ holder: string; you: boolean } | null>(null);
  // Change-only capture (mpdecimate): suppress unchanged frames, still send
  // every changed one. Toggling restarts the agent's pipeline.
  const [differential, setDifferential] = useState(false);
  // Live audio: the Opus track is always negotiated; this flag starts/stops the
  // target's capture. The element starts muted because browsers block autoplay
  // with sound until the operator interacts.
  const [audioOn, setAudioOn] = useState(false);
  const [audioMuted, setAudioMuted] = useState(true);
  const [audioVolume, setAudioVolume] = useState(0.7);
  const [audioError, setAudioError] = useState("");
  const [audioMode, setAudioMode] = useState<"mic" | "speaker">("mic");
  const [audioDevice, setAudioDevice] = useState(0);
  // The capture source the agent actually resolved (a dshow device or a
  // PulseAudio source name), so "speaker" is verifiable and not just a flag.
  const [audioSource, setAudioSource] = useState("");
  const [audioQuality, setAudioQuality] = useState<AudioQuality>("standard");
  // Device names the agent enumerated for the current mode; empty until the
  // first list arrives (then the console offers names instead of an index).
  const [audioDevices, setAudioDevices] = useState<AudioDeviceInfo[]>([]);
  // Freshness of each list, so opening a picker re-enumerates only when the
  // target may have changed (see deviceListStale).
  const [audioDevicesAt, setAudioDevicesAt] = useState(0);
  const [audioDevicesMode, setAudioDevicesMode] = useState<"mic" | "speaker">("mic");
  // Region mode replaces H.264 video with change-only tiles drawn on a canvas:
  // bandwidth tracks the changed area instead of the frame size, and no media
  // plane is used at all (useful on TCP-only or ffmpeg-less targets).
  // screen = the desktop (interactive); webcam = the target camera (view-only).
  const [videoSource, setVideoSource] = useState<VideoSource>("screen");
  const [webcamDevice, setWebcamDevice] = useState(0);
  const [videoDevices, setVideoDevices] = useState<VideoDeviceInfo[]>([]);
  const [videoDevicesAt, setVideoDevicesAt] = useState(0);
  const [regionOn, setRegionOn] = useState(false);
  const [tileFrame, setTileFrame] = useState<TileFrame | null>(null);
  const [regionError, setRegionError] = useState("");
  const audioRef = useRef<HTMLAudioElement>(null);
  const pendingAudioTrackRef = useRef<MediaStreamTrack | null>(null);
  const inputErrTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const noteInputError = useCallback((reason: string) => {
    setInputError(reason);
    if (inputErrTimerRef.current) clearTimeout(inputErrTimerRef.current);
    inputErrTimerRef.current = setTimeout(() => {
      inputErrTimerRef.current = null;
      setInputError("");
    }, 4000);
  }, []);

  const videoRef = useRef<HTMLVideoElement>(null);
  // Region mode draws tiles into this canvas instead of a <video>; input
  // mapping uses its pixel dimensions (like videoWidth/videoHeight).
  const regionCanvasRef = useRef<HTMLCanvasElement>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const activeRef = useRef(false);
  const stoppingRef = useRef(false);
  const pendingLocalCandidatesRef = useRef<RTCIceCandidateInit[]>([]);
  const pendingRemoteCandidatesRef = useRef<RTCIceCandidateInit[]>([]);
  const answerTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const sessionIdRef = useRef("");

  const presets = useMemo(
    () =>
      LIVE_PRESETS.map((preset) => ({
        ...preset,
        label:
          preset.value === "1080p"
            ? t("agents.rdp_live_1080p")
            : t("agents.rdp_live_720p"),
      })),
    [t],
  );

  const clearAnswerTimer = useCallback(() => {
    if (answerTimerRef.current) {
      clearTimeout(answerTimerRef.current);
      answerTimerRef.current = null;
    }
  }, []);

  const cleanupLocal = useCallback(() => {
    activeRef.current = false;
    setActive(false);
    clearAnswerTimer();
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws) {
      ws.onopen = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
      try {
        ws.close();
      } catch {
        /* already closed */
      }
    }
    const pc = pcRef.current;
    pcRef.current = null;
    if (pc) {
      pc.ontrack = null;
      pc.onicecandidate = null;
      pc.onconnectionstatechange = null;
      try {
        pc.close();
      } catch {
        /* already closed */
      }
    }
    const video = videoRef.current;
    if (video) {
      video.srcObject = null;
    }
    const audio = audioRef.current;
    if (audio) {
      audio.srcObject = null;
    }
    pendingAudioTrackRef.current = null;
    pendingLocalCandidatesRef.current = [];
    pendingRemoteCandidatesRef.current = [];
  }, [clearAnswerTimer]);

  const fail = useCallback(
    (message: string) => {
      const sessionId = sessionIdRef.current;
      sessionIdRef.current = "";
      cleanupLocal();
      setStatus("error");
      setError(message);
      // A failure can happen before the operator socket opens; in that case
      // closing the socket cannot end the server session, so use the
      // id-scoped fallback stop.
      if (agentId && sessionId) {
        void api
          .postJson(paths.agents.webrtcStop(agentId), { session_id: sessionId })
          .catch(() => {});
      }
    },
    [agentId, cleanupLocal],
  );

  const sendSignal = useCallback((message: Record<string, unknown>): boolean => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    try {
      ws.send(JSON.stringify(message));
      return true;
    } catch {
      return false;
    }
  }, []);

  const sendInput = useCallback(
    (event: RemoteInputEvent): boolean => sendSignal({ type: "input", event }),
    [sendSignal],
  );

  const stop = useCallback(async () => {
    const sessionId = sessionIdRef.current;
    sessionIdRef.current = "";
    stoppingRef.current = true;
    sendSignal({ type: "stop" });
    cleanupLocal();
    setStatus("idle");
    setError("");
    setClipboard(null);
    setClipboardError("");
    setClipboardHistory([]);
    setKeywordAlerts([]);
    setKeywordWatchOn(false);
    if (agentId) {
      // Fallback for an agent whose signaling socket died before it saw the
      // close: the server queues screen_webrtc_stop. The session id keeps a
      // delayed fallback from stopping a newer session on the same agent.
      await api
        .postJson(paths.agents.webrtcStop(agentId), sessionId ? { session_id: sessionId } : {})
        .catch(() => {});
    }
    stoppingRef.current = false;
  }, [agentId, cleanupLocal, sendSignal]);

  const requestClipboard = useCallback(() => {
    if (!sendSignal({ type: "clipboard_get" })) setClipboardError(t("agents.rdp_clipboard_failed"));
  }, [sendSignal, t]);

  const pushClipboard = useCallback((text: string) => {
    if (!sendSignal({ type: "clipboard_set", text })) setClipboardError(t("agents.rdp_clipboard_failed"));
  }, [sendSignal, t]);

  const sendMessage = useCallback((text: string, title?: string) => {
    const clean = text.trim();
    if (!clean) return false;
    setMessageStatus("");
    return sendSignal({ type: "message", text: clean, ...(title ? { title } : {}) });
  }, [sendSignal]);

  const toggleKeys = useCallback((enabled: boolean) => {
    if (enabled) setKeysFeed("");
    if (!sendSignal({ type: "keys", enabled })) setKeysOn(false);
  }, [sendSignal]);

  const configureKeywordWatch = useCallback((opts: {
    enabled: boolean;
    keywords: string[];
    minIntervalMs?: number;
    clipboard?: boolean;
    windowTitle?: boolean;
  }): boolean => {
    const ok = sendSignal({
      type: "keyword_watch",
      keywords: opts.keywords,
      min_interval_ms: opts.minIntervalMs ?? 2000,
      clipboard: opts.clipboard ?? true,
      window_title: opts.windowTitle ?? false,
    });
    if (ok) {
      setKeywordWatchOn(opts.enabled && opts.keywords.length > 0);
      if (!opts.enabled) setKeywordAlerts([]);
    }
    return ok;
  }, [sendSignal]);

  const takeControl = useCallback(() => {
    return sendSignal({ type: "control", action: "take" });
  }, [sendSignal]);

  const releaseControl = useCallback(() => {
    return sendSignal({ type: "control", action: "release" });
  }, [sendSignal]);

  const toggleDifferential = useCallback((enabled: boolean) => {
    setDifferential(enabled);
    sendSignal({ type: "differential", enabled });
  }, [sendSignal]);

  const toggleAudio = useCallback((enabled: boolean) => {
    setAudioError("");
    setAudioOn(enabled);
    if (enabled) setAudioMuted(false);
    sendSignal({ type: "audio", enabled, mode: audioMode, device: audioDevice, quality: audioQuality });
  }, [sendSignal, audioMode, audioDevice]);

  // A source change while audio is running restarts only the capture process;
  // the negotiated Opus track stays as it is.
  const selectAudioSource = useCallback((mode: "mic" | "speaker", device: number) => {
    setAudioMode(mode);
    setAudioDevice(device);
    if (audioOn) sendSignal({ type: "audio", enabled: true, mode, device, quality: audioQuality });
    // The candidate list is per mode (microphones vs loopback devices), so a
    // mode change must ask the target again.
    setAudioDevicesAt(Date.now());
    setAudioDevicesMode(mode);
    sendSignal({ type: "audio_devices", mode });
  }, [sendSignal, audioOn, audioQuality]);

  // A quality change while audio is running restarts only the capture process
  // (the Opus track and its negotiated m-line are untouched).
  const selectAudioQuality = useCallback((quality: AudioQuality) => {
    setAudioQuality(quality);
    if (audioOn) {
      sendSignal({ type: "audio", enabled: true, mode: audioMode, device: audioDevice, quality });
    }
  }, [sendSignal, audioOn, audioMode, audioDevice]);

  const requestAudioDevices = useCallback((mode: "mic" | "speaker", force = false) => {
    // The list is per mode and ages out: only re-enumerate when the operator
    // asked, switched mode, or the cached list is stale.
    if (!force && mode === audioDevicesMode && !deviceListStale(audioDevicesAt, Date.now())) {
      return;
    }
    setAudioDevicesAt(Date.now());
    setAudioDevicesMode(mode);
    sendSignal({ type: "audio_devices", mode });
  }, [sendSignal, audioDevicesMode, audioDevicesAt]);

  const requestVideoDevices = useCallback((force = false) => {
    if (!force && !deviceListStale(videoDevicesAt, Date.now())) {
      return;
    }
    setVideoDevicesAt(Date.now());
    sendSignal({ type: "video_devices" });
  }, [sendSignal, videoDevicesAt]);

  // Monitor hotplug: re-enumerate when the picker is opened and the last
  // announcement is older than the TTL (or the operator forces it).
  const requestDisplays = useCallback((force = false) => {
    if (!force && !deviceListStale(displaysAt, Date.now())) {
      return;
    }
    setDisplaysAt(Date.now());
    sendSignal({ type: "display_list" });
  }, [sendSignal, displaysAt]);

  // Switching to the camera asks for its device list: the index the operator
  // picks must exist on the target before the session starts.
  const selectVideoSource = useCallback((source: VideoSource) => {
    setVideoSource(source);
    if (source === "webcam") {
      setVideoDevicesAt(Date.now());
      sendSignal({ type: "video_devices" });
    }
  }, [sendSignal]);

  const toggleRegion = useCallback((enabled: boolean) => {
    setRegionError("");
    setRegionOn(enabled);
    if (enabled) setTileFrame(null);
    sendSignal({ type: "region", enabled });
  }, [sendSignal]);

  // Keep the audio element's volume/mute in sync with the controls.
  useEffect(() => {
    const el = audioRef.current;
    if (!el) return;
    el.volume = audioVolume;
    el.muted = audioMuted;
  }, [audioVolume, audioMuted]);

  // A track can arrive before the audio element is mounted; attach it then.
  useEffect(() => {
    const el = audioRef.current;
    const track = pendingAudioTrackRef.current;
    if (!el || !track) return;
    el.srcObject = new MediaStream([track]);
    pendingAudioTrackRef.current = null;
    void el.play().catch(() => {});
  }, [audioOn]);

  // The browser clipboard API needs a secure context and a user gesture. When
  // it is unavailable (plain-HTTP deployment, permission denied) the console
  // reports it and the manual paste box stays usable.
  const pasteLocalClipboard = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText();
      pushClipboard(text);
    } catch {
      setClipboardError(t("agents.rdp_clipboard_denied"));
    }
  }, [pushClipboard, t]);

  const copyClipboardToLocal = useCallback(async () => {
    if (!clipboard) return;
    if (await copyText(clipboard.text)) {
      setClipboardError("");
    } else {
      setClipboardError(t("agents.rdp_clipboard_denied"));
    }
  }, [clipboard, t]);

  const requestClipboardHistory = useCallback(() => {
    sendSignal({ type: "clipboard_history", limit: 20 });
  }, [sendSignal]);

  const loadHistoryItem = useCallback((index: number) => {
    sendSignal({ type: "clipboard_history_get", index });
  }, [sendSignal]);

  const selectDisplay = useCallback((display: number) => {
    setSelectedDisplay(display);
    // While a session is live the agent restarts its capture against the new
    // monitor; before a session the choice is simply remembered for the start
    // request.
    if (activeRef.current) sendSignal({ type: "display", display });
  }, [sendSignal]);

  const start = useCallback(
    async (presetValue: string, display = 0, differentialMode = false, audioEnabled = false, regionMode = false, source: VideoSource = "screen") => {
      if (!agentId) return;
      if (typeof RTCPeerConnection === "undefined" || typeof WebSocket === "undefined") {
        fail(t("agents.rdp_live_unsupported"));
        return;
      }
      if (activeRef.current) {
        await stop();
      }
      const preset = getLivePreset(presetValue);
      setLivePreset(preset.value);
      setLiveWidth(preset.width);
      setLiveHeight(preset.height);
      setSelectedDisplay(display);
      setDifferential(differentialMode);
      setAudioOn(audioEnabled);
      setVideoSource(source);
      setRegionOn(regionMode);
      setTileFrame(null);
      setRegionError("");
      setError("");
      setStatus("connecting");

      let response: StartResponse;
      try {
        response = await api.postJson<StartResponse>(paths.agents.webrtcStart(agentId), {
          width: preset.width,
          height: preset.height,
          fps: preset.fps,
          bitrate_kbps: preset.bitrateKbps,
          encoder: "auto",
          display,
          differential: differentialMode,
          audio: audioEnabled,
          audio_mode: audioMode,
          audio_device: audioDevice,
          audio_quality: audioQuality,
          region: regionMode,
          source,
          webcam_device: webcamDevice,
        });
      } catch (err) {
        setStatus("error");
        setError(formatThrownError(err));
        throw err;
      }
      const sessionId = String(response.session_id || "");
      if (!sessionId) {
        setStatus("error");
        setError(t("agents.rdp_live_start_failed"));
        return;
      }
      sessionIdRef.current = sessionId;

      let pc: RTCPeerConnection;
      try {
        pc = new RTCPeerConnection({ iceServers: normalizeIceServers(response.ice_servers) });
      } catch (err) {
        fail(formatThrownError(err));
        return;
      }
      pcRef.current = pc;
      activeRef.current = true;
      setActive(true);
      pc.addTransceiver("video", { direction: "recvonly" });
      // The agent always negotiates an audio m-line, so the console can turn
      // audio on later without a renegotiation round trip.
      pc.addTransceiver("audio", { direction: "recvonly" });

      pc.ontrack = (event) => {
        if (event.track.kind === "audio") {
          const el = audioRef.current;
          if (el) {
            el.srcObject = new MediaStream([event.track]);
            void el.play().catch(() => {});
          } else {
            pendingAudioTrackRef.current = event.track;
          }
          return;
        }
        const video = videoRef.current;
        if (!video) return;
        video.srcObject = event.streams[0] ?? new MediaStream([event.track]);
        video.muted = true;
        void video.play().catch(() => {});
      };
      pc.onicecandidate = (event) => {
        if (!event.candidate) return;
        const candidate = event.candidate.toJSON();
        const ws = wsRef.current;
        if (ws?.readyState === WebSocket.OPEN) {
          sendSignal({ type: "ice", candidate });
        } else {
          pendingLocalCandidatesRef.current.push(candidate);
        }
      };
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === "failed") {
          fail(t("agents.rdp_live_connection_failed"));
        }
      };

      let ws: WebSocket;
      try {
        ws = new WebSocket(buildOperatorWsUrl(sessionId));
      } catch {
        fail(t("agents.rdp_live_start_failed"));
        return;
      }
      wsRef.current = ws;
      ws.onopen = () => {
        void (async () => {
          try {
            const offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            const sdp = pc.localDescription?.sdp ?? offer.sdp ?? "";
            ws.send(JSON.stringify({ type: "offer", sdp }));
            for (const candidate of pendingLocalCandidatesRef.current) {
              ws.send(JSON.stringify({ type: "ice", candidate }));
            }
            pendingLocalCandidatesRef.current = [];
            // Ask for the target's capture devices so the audio strip can show
            // names (a macOS loopback index is otherwise undiscoverable).
            setAudioDevicesAt(Date.now());
            setVideoDevicesAt(Date.now());
            setDisplaysAt(Date.now());
            ws.send(JSON.stringify({ type: "audio_devices", mode: audioMode }));
            ws.send(JSON.stringify({ type: "video_devices" }));
            clearAnswerTimer();
            answerTimerRef.current = setTimeout(() => {
              if (activeRef.current && pc.remoteDescription === null) {
                fail(t("agents.rdp_live_timeout"));
              }
            }, 20_000);
          } catch (err) {
            fail(formatThrownError(err));
          }
        })();
      };
      ws.onmessage = (event) => {
        let message: SignalMessage;
        try {
          message = JSON.parse(String(event.data)) as SignalMessage;
        } catch {
          return;
        }
        switch (message.type) {
          case "answer":
            if (!message.sdp) return;
            clearAnswerTimer();
            void pc
              .setRemoteDescription({ type: "answer", sdp: message.sdp })
              .then(async () => {
                const queued = pendingRemoteCandidatesRef.current;
                pendingRemoteCandidatesRef.current = [];
                for (const candidate of queued) {
                  await pc.addIceCandidate(candidate).catch(() => {});
                }
              })
              .catch((err) => fail(formatThrownError(err)));
            break;
          case "ice":
            if (!message.candidate) return;
            if (pc.remoteDescription === null) {
              pendingRemoteCandidatesRef.current.push(message.candidate);
            } else {
              void pc.addIceCandidate(message.candidate).catch(() => {});
            }
            break;
          case "state": {
            if (message.state === "connected" || message.state === "streaming") {
              setStatus("connected");
              break;
            }
            const parsed = parseDisplayState(message);
            if (parsed) {
              setDisplays(parsed.displays);
              setSelectedDisplay(parsed.selected);
            } else if (message.state === "display" && typeof message.selected === "number") {
              setSelectedDisplay(message.selected);
            }
            break;
          }
          case "waiting":
            setStatus("connecting");
            break;
          case "error":
            fail(message.reason || message.error || t("agents.rdp_live_connection_failed"));
            break;
          case "input_error":
          case "input_rejected":
            noteInputError(message.reason || message.error || t("agents.rdp_input_rejected"));
            break;
          case "clipboard":
            if (typeof message.text === "string") {
              setClipboard({
                text: message.text,
                source: message.source === "operator" ? "operator" : "agent",
                truncated: message.truncated === true,
                at: Date.now(),
              });
              setClipboardError("");
            }
            break;
          case "clipboard_error":
            setClipboardError(message.reason || message.error || t("agents.rdp_clipboard_failed"));
            break;
          case "clipboard_history": {
            const items = Array.isArray(message.items) ? message.items : [];
            setClipboardHistory(
              items
                .filter((item): item is ClipboardHistoryItem =>
                  typeof item === "object" && item !== null && typeof (item as ClipboardHistoryItem).index === "number")
                .map((item) => ({
                  index: Number(item.index),
                  preview: String(item.preview ?? ""),
                  bytes: Number(item.bytes ?? 0),
                  at: String(item.at ?? ""),
                })),
            );
            break;
          }
          case "message_status":
            setMessageStatus(message.status === "shown" ? "shown" : "failed");
            break;
          case "keys":
            if (typeof message.enabled === "boolean") setKeysOn(message.enabled);
            if (typeof message.text === "string") {
              setKeysFeed((prev) => appendKeyFeed(prev, message.text as string));
            }
            break;
          case "keyword_watch":
            if (typeof message.enabled === "boolean") setKeywordWatchOn(message.enabled);
            break;
          case "keyword_alert": {
            if (typeof message.term !== "string") break;
            const alert: KeywordAlert = {
              source: typeof message.source === "string" ? message.source : "",
              term: message.term,
              preview: typeof message.preview === "string" ? message.preview : "",
              ts: typeof message.ts === "number" ? message.ts : Date.now() / 1000,
            };
            setKeywordAlerts((prev) => [alert, ...prev].slice(0, 50));
            break;
          }
          case "control": {
            const holder = typeof message.holder === "string" ? message.holder : "";
            setControl(holder ? { holder, you: message.you === true } : null);
            break;
          }
          case "differential":
            if (typeof message.enabled === "boolean") setDifferential(message.enabled);
            break;
          case "audio":
            if (typeof message.enabled === "boolean") setAudioOn(message.enabled);
            setAudioError(typeof message.reason === "string" ? message.reason : "");
            if (typeof message.source === "string" && message.source) setAudioSource(message.source);
            if (message.enabled === false) setAudioSource("");
            break;
          case "video_devices": {
            const items = Array.isArray(message.items) ? message.items : [];
            setVideoDevices(
              items
                .filter((item): item is VideoDeviceInfo =>
                  typeof item === "object" && item !== null &&
                  typeof (item as VideoDeviceInfo).index === "number" &&
                  typeof (item as VideoDeviceInfo).name === "string")
                .map((item) => ({ index: Number(item.index), name: String(item.name) })),
            );
            setError(typeof message.reason === "string" ? message.reason : "");
            break;
          }
          case "audio_devices": {
            const items = Array.isArray(message.items) ? message.items : [];
            setAudioDevices(
              items
                .filter((item): item is AudioDeviceInfo =>
                  typeof item === "object" && item !== null &&
                  typeof (item as AudioDeviceInfo).index === "number" &&
                  typeof (item as AudioDeviceInfo).name === "string")
                .map((item) => ({
                  index: Number(item.index),
                  name: String(item.name),
                  loopback: item.loopback === true,
                })),
            );
            if (typeof message.reason === "string" && message.reason) {
              setAudioError(message.reason);
            }
            break;
          }
          case "region":
            if (typeof message.enabled === "boolean") setRegionOn(message.enabled);
            setRegionError(typeof message.reason === "string" ? message.reason : "");
            break;
          case "tiles": {
            const frame = parseTileFrame(message);
            if (!frame) break;
            setTileFrame(frame);
            // The agent confirms region mode with its first frame; a session
            // that was started in this mode should show "connected" even though
            // no media track ever connects.
            setStatus("connected");
            break;
          }
          case "session_end": {
            if (stoppingRef.current) return;
            fail(message.reason ? `${t("agents.rdp_live_ended")} (${message.reason})` : t("agents.rdp_live_ended"));
            break;
          }
        }
      };
      ws.onerror = () => {
        if (activeRef.current && !stoppingRef.current) {
          fail(t("agents.rdp_live_connection_failed"));
        }
      };
      ws.onclose = () => {
        if (activeRef.current && !stoppingRef.current) {
          fail(t("agents.rdp_live_ended"));
        }
      };
    },
    [agentId, audioMode, audioDevice, clearAnswerTimer, fail, noteInputError, sendSignal, stop, t],
  );

  const markPlaying = useCallback(() => {
    if (activeRef.current) setStatus("connected");
  }, []);

  // Closing the signaling socket ends the server session, which tells the
  // agent to kill ffmpeg. No API call is made from unload paths.
  useEffect(() => {
    return () => {
      if (activeRef.current) cleanupLocal();
      if (inputErrTimerRef.current) {
        clearTimeout(inputErrTimerRef.current);
        inputErrTimerRef.current = null;
      }
    };
  }, [cleanupLocal]);

  useEffect(() => {
    if (!agentId) return;
    cleanupLocal();
    setStatus("idle");
    setError("");
    setInputError("");
    setClipboard(null);
    setClipboardError("");
    setClipboardHistory([]);
    setMessageStatus("");
    setKeysOn(false);
    setKeysFeed("");
    setControl(null);
    setAudioSource("");
    setAudioDevices([]);
    setRegionOn(false);
    setTileFrame(null);
    setRegionError("");
  }, [agentId, cleanupLocal]);

  return {
    t,
    active,
    status,
    error,
    inputError,
    clipboard,
    clipboardError,
    requestClipboard,
    pushClipboard,
    clipboardHistory,
    requestClipboardHistory,
    loadHistoryItem,
    pasteLocalClipboard,
    copyClipboardToLocal,
    messageStatus,
    sendMessage,
    keysOn,
    keysFeed,
    toggleKeys,
    keywordAlerts,
    keywordWatchOn,
    configureKeywordWatch,
    control,
    takeControl,
    releaseControl,
    differential,
    toggleDifferential,
    audioOn,
    audioMuted,
    audioVolume,
    audioError,
    audioRef,
    toggleAudio,
    audioMode,
    audioDevice,
    audioSource,
    audioQuality,
    selectAudioQuality,
    audioDevices,
    requestAudioDevices,
    selectAudioSource,
    videoSource,
    setVideoSource: selectVideoSource,
    webcamDevice,
    setWebcamDevice,
    videoDevices,
    requestVideoDevices,
    requestDisplays,
    regionOn,
    regionError,
    tileFrame,
    toggleRegion,
    regionCanvasRef,
    setAudioMuted,
    setAudioVolume,
    displays,
    selectedDisplay,
    selectDisplay,
    presets,
    livePreset,
    setLivePreset,
    liveWidth,
    liveHeight,
    videoRef,
    start,
    stop,
    sendInput,
    markPlaying,
  };
}
