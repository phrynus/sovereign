
import { memo, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import { toneStyles, type Tone } from "@/lib/ui/statusStyles";

const CHIP_TONE: Record<string, string> = {
  neutral: "border-border/70 bg-muted/30",
  success: "border-success/20 bg-success/10",
  warning: "border-warning/25 bg-warning/10",
  destructive: "border-destructive/25 bg-destructive/10",
};

interface MetaChipProps {
  /** Uppercase key label (e.g. IP, OS, AV). */
  label: string;
  /** The value — may include a CopyButton or other inline control. */
  children: ReactNode;
  tone?: "neutral" | "success" | "warning" | "destructive";
  icon?: ReactNode;
  /** Color of the label/value text; defaults to muted for the key. */
  labelClass?: string;
  valueClass?: string;
  title?: string;
  className?: string;
}

/**
 * Shared key/value pill. Centralizes the `inline-flex items-center gap-1.5
 * rounded-full border px-2.5 py-1` shape used throughout agent headers and
 * detail cards, so padding/radius/border stay consistent and tones come from
 * the design tokens. Lives in the lazy detail chunks — no entry impact.
 */
export const MetaChip = memo(function MetaChip({
  label,
  children,
  tone = "neutral",
  icon,
  labelClass,
  valueClass,
  title,
  className,
}: MetaChipProps) {
  const cfg = toneStyles[(tone === "neutral" ? "muted" : tone) as Tone];
  const accent = tone === "neutral" ? "text-muted-foreground" : cfg.text;
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1", CHIP_TONE[tone], className)} title={title}>
      {icon}
      <span className={cn("text-micro-sm uppercase tracking-wide", labelClass ?? accent)}>{label}</span>
      <span className={cn("text-foreground", valueClass)}>{children}</span>
    </span>
  );
});
