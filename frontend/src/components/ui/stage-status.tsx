
import { memo, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { toneStyles, type Tone } from "@/lib/ui/statusStyles";

interface StageStatusProps {
  /** Semantic tone — the dot and error text color derive from the design tokens. */
  tone: Tone;
  /** Translated status label. */
  label: string;
  /** Optional leading indicator (Spinner / TriangleAlert) shown after the dot. */
  icon?: ReactNode;
  /** Use the tone-agnostic pulse animation. */
  pulse?: boolean;
  className?: string;
}

/**
 * Shared status cluster for the screen/RDP stage headers. Previously
 * `ScreenViewer` and `RdpStage` each built their own near-identical
 * `{color, text, icon}` object and markup; this primitive centralizes it so
 * both stages stay theme-aware and consistent. Colors come from `toneStyles`
 * (never hardcoded), and the shape is intentionally tiny so it lives in the
 * lazy detail chunks without affecting the first-paint bundle.
 */
export const StageStatus = memo(function StageStatus({
  tone,
  label,
  icon,
  pulse = false,
  className,
}: StageStatusProps) {
  const cfg = toneStyles[tone];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5",
        tone === "destructive" && "text-destructive-ink",
        className
      )}
    >
      <span
        aria-hidden="true"
        className={cn("size-2 rounded-full", cfg.dot, pulse && "animate-pulse")}
      />
      {icon}
      <span>{label}</span>
    </span>
  );
});
