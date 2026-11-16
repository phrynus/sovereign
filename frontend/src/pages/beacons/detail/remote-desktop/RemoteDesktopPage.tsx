import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
import { enGenerate } from "@/lib/i18n/fragments/en.generate";
registerFragment(enAgents);
registerFragment(enGenerate);
import { useParams } from "react-router-dom";
import { Banner } from "@/components/ui/banner";
import { PageContainer } from "@/components/ui/page-container";
import { TriangleAlert } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { useRemoteDesktop } from "./components/useRemoteDesktop";
import RdpToolbar from "./components/RdpToolbar";
import RdpStage from "./components/RdpStage";
import RdpStatusCards from "./components/RdpStatusCards";
import RdpClipboard from "./components/RdpClipboard";
import RdpMessage from "./components/RdpMessage";
import RdpKeys from "./components/RdpKeys";
import RdpControl from "./components/RdpControl";
import RdpAudio from "./components/RdpAudio";

export default function RemoteDesktopPage() {
  const { t } = useI18n();
  const params = useParams();
  const rawId = params.id as string | string[];
  const id = Array.isArray(rawId) ? rawId[0] : rawId || "";

  const rdp = useRemoteDesktop(id);

  return (
    <PageContainer variant="workspace" className="h-full">
      <RdpToolbar
        t={t}
        agentId={id}
        nativeWidth={rdp.liveActive ? rdp.liveWidth : rdp.nativeWidth}
        nativeHeight={rdp.liveActive ? rdp.liveHeight : rdp.nativeHeight}
        resolutions={rdp.RESOLUTIONS}
        resolution={rdp.resolution}
        setResolution={rdp.setResolution}
        mode={rdp.mode}
        setMode={rdp.setMode}
        livePresets={rdp.livePresets}
        livePreset={rdp.livePreset}
        setLivePreset={rdp.setLivePreset}
        displays={rdp.displays}
        selectedDisplay={rdp.selectedDisplay}
        onSelectDisplay={rdp.selectDisplay}
        videoSource={rdp.videoSource}
        onSelectVideoSource={rdp.setVideoSource}
        differential={rdp.differential}
        onToggleDifferential={rdp.toggleDifferential}
        regionOn={rdp.regionOn}
        onToggleRegion={rdp.toggleRegion}
        active={rdp.sessionActive}
        isFullscreen={rdp.isFullscreen}
        onToggleFullscreen={() => void rdp.toggleFullscreen()}
        versionBlocked={rdp.versionBlocked}
        onStart={rdp.handleStart}
        onStop={rdp.handleStop}
      />
      <div className="flex min-h-0 flex-1 flex-col gap-3 px-(--page-pad-x) py-(--page-pad-y)">
        <Banner tone="warning" icon={<TriangleAlert className="size-4" />} className="items-start">
          <div className="font-semibold">{t("agents.rdp_experimental_title")}</div>
          <div className="text-xs text-muted-foreground">{t("agents.rdp_experimental_desc")}</div>
        </Banner>
        <div className="flex min-h-0 flex-1 flex-col">
          <RdpStage
            t={t}
            status={rdp.status}
            monitoring={rdp.inputEnabled}
            live={rdp.liveActive}
            liveMode={rdp.mode === "live"}
            lastUpdate={rdp.lastUpdate}
            wsLive={rdp.wsLive}
            screenData={rdp.screenData}
            liveError={rdp.liveError}
            liveInputError={rdp.liveInputError}
            nativeWidth={rdp.nativeWidth}
            nativeHeight={rdp.nativeHeight}
            mouseX={rdp.mouseX}
            mouseY={rdp.mouseY}
            showCursor={rdp.showCursor}
            containerRef={rdp.containerRef}
            imgRef={rdp.imgRef}
            videoRef={rdp.liveVideoRef}
            regionCanvasRef={rdp.regionCanvasRef}
            regionOn={rdp.regionOn}
            cameraView={rdp.videoSource === "webcam"}
            regionError={rdp.regionError}
            tileFrame={rdp.tileFrame}
            onImageLoad={(w, h) => { rdp.setNativeWidth(w); rdp.setNativeHeight(h); }}
            onVideoPlaying={rdp.markLivePlaying}
            onClick={(e) => void rdp.handleClick(e)}
            onContextMenu={rdp.handleContextMenu}
            onMouseDown={rdp.handleMouseDown}
            onWheel={rdp.handleWheel}
            onMouseMove={rdp.handleMouseMove}
            onMouseLeave={rdp.hideCursor}
          />

          <RdpStatusCards t={t} monitoring={rdp.monitoring} />
          {rdp.liveActive && (
            <div className="mt-3">
              <RdpClipboard
                t={t}
                clipboard={rdp.clipboard}
                error={rdp.clipboardError}
                onRead={rdp.requestClipboard}
                onCopyToLocal={() => void rdp.copyClipboardToLocal()}
                onPasteLocal={() => void rdp.pasteLocalClipboard()}
                onSendText={rdp.pushClipboard}
                history={rdp.clipboardHistory}
                onRequestHistory={rdp.requestClipboardHistory}
                onLoadHistory={rdp.loadHistoryItem}
              />
              <RdpMessage t={t} status={rdp.messageStatus} onSend={rdp.sendMessage} />
              <RdpKeys t={t} enabled={rdp.keysOn} feed={rdp.keysFeed} onToggle={rdp.toggleKeys} />
              <RdpAudio
                t={t}
                enabled={rdp.audioOn}
                muted={rdp.audioMuted}
                volume={rdp.audioVolume}
                error={rdp.audioError}
                mode={rdp.audioMode}
                device={rdp.audioDevice}
                source={rdp.audioSource}
                devices={rdp.audioDevices}
                onRefresh={rdp.requestAudioDevices}
                audioRef={rdp.audioRef}
                onToggle={rdp.toggleAudio}
                onSource={rdp.selectAudioSource}
                onMute={rdp.setAudioMuted}
                onVolume={rdp.setAudioVolume}
              />
              <div className="mt-2">
                <RdpControl
                  t={t}
                  control={rdp.control}
                  onTake={() => void rdp.takeControl()}
                  onRelease={() => void rdp.releaseControl()}
                />
              </div>
            </div>
          )}
        </div>
      </div>
    </PageContainer>
  );
}
