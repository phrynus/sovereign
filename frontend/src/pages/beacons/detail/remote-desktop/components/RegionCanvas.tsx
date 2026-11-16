import { memo, useEffect, useRef, type RefObject } from "react";
import type { TileFrame } from "./useWebRTCLive";

/** Prefix every tile payload uses: the agent JPEG-encodes each changed block. */
export const TILE_IMAGE_PREFIX = "data:image/jpeg;base64,";

/** Source URL for one tile's base64 JPEG. */
export function tileSource(data: string): string {
  return `${TILE_IMAGE_PREFIX}${data}`;
}

/**
 * Decoded byte size of a tile frame's payloads. Base64 carries 3 bytes per 4
 * characters (minus padding), so this is what the session actually costs on
 * the wire rather than the JSON length.
 */
export function tileFrameBytes(frame: TileFrame | null): number {
  if (!frame) return 0;
  let total = 0;
  for (const tile of frame.tiles) {
    const len = tile.data.length;
    const padding = tile.data.endsWith("==") ? 2 : tile.data.endsWith("=") ? 1 : 0;
    total += Math.max(0, Math.floor((len * 3) / 4) - padding);
  }
  return total;
}

interface RegionCanvasProps {
  frame: TileFrame | null;
  canvasRef: RefObject<HTMLCanvasElement | null>;
  label: string;
  waitingLabel: string;
}

/**
 * Change-only tile canvas. Every frame carries just the blocks that changed;
 * the canvas keeps the previous paint, so unchanged areas stay as they were.
 * Tile images load asynchronously, so a sequence number drops a tile from an
 * older frame that arrives after a newer frame has already started painting.
 */
export default memo(function RegionCanvas({ frame, canvasRef, label, waitingLabel }: RegionCanvasProps) {
  const seqRef = useRef(0);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || !frame) return;
    // Resizing clears the canvas; do it only when the capture size changed.
    if (canvas.width !== frame.width || canvas.height !== frame.height) {
      canvas.width = frame.width;
      canvas.height = frame.height;
    }
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    if (frame.full) ctx.clearRect(0, 0, frame.width, frame.height);

    const seq = seqRef.current + 1;
    seqRef.current = seq;
    for (const tile of frame.tiles) {
      const img = new Image();
      img.onload = () => {
        // A tile from a superseded frame must not overwrite newer pixels.
        if (seqRef.current !== seq) return;
        ctx.drawImage(img, tile.x, tile.y, tile.w, tile.h);
      };
      img.src = tileSource(tile.data);
    }
  }, [frame, canvasRef]);

  const bytes = tileFrameBytes(frame);
  return (
    <>
      <canvas
        ref={canvasRef}
        className="max-w-full max-h-full object-contain bg-black"
        width={frame?.width ?? 1280}
        height={frame?.height ?? 720}
        aria-label={label}
        role="img"
      />
      <div className="absolute top-2 right-2 rounded bg-black/60 px-2 py-1 text-micro-sm text-white/80 pointer-events-none">
        {frame
          ? `${frame.tiles.length} tile${frame.tiles.length === 1 ? "" : "s"} · ${(bytes / 1024).toFixed(1)} KiB`
          : waitingLabel}
      </div>
    </>
  );
});
