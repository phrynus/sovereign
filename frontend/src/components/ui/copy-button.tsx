
import { useEffect, useRef, useState } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { toast } from "sonner";
import { copyText } from "@/lib/clipboard";

export function CopyButton({ text, label, title, className, size = "icon-xs", children, onError }: {
  text: string;
  label?: string;
  title?: string;
  className?: string;
  size?: "icon-xs" | "xs" | "sm";
  children?: React.ReactNode | ((copied: boolean) => React.ReactNode);
  onError?: () => void;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const mountedRef = useRef(true);
  const copyingRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (timerRef.current !== null) clearTimeout(timerRef.current);
    };
  }, []);
  // Clipboard mechanism lives in lib/clipboard so every copy action shares one
  // implementation (async API + execCommand fallback). The toast stays here.
  const finish = (ok: boolean) => {
    if (!mountedRef.current) return;
    if (ok) {
      if (timerRef.current !== null) clearTimeout(timerRef.current);
      setCopied(true);
      timerRef.current = setTimeout(() => {
        timerRef.current = null;
        setCopied(false);
      }, 1500);
    } else {
      setCopied(false);
      onError?.();
      toast.error(t("common.copy_failed"));
    }
    copyingRef.current = false;
  };
  const copy = () => {
    if (copyingRef.current) return;
    copyingRef.current = true;
    const result = copyText(text);
    if (typeof result === "boolean") finish(result); // synchronous fallback path
    else result.then(finish); // async Clipboard API path
  };

  const defaultIcon = copied
    ? <Check className="size-2.5 text-success-ink" />
    : <Copy className="size-2.5" />;
  const copyLabel = label ? t("common.copy_value").replace("{value}", label) : t("common.copy");

  return (
    <>
    <Tooltip>
      <TooltipTrigger render={<Button variant="ghost" size={size} onClick={copy} className={className || "ml-1 text-muted-foreground hover:text-foreground"} aria-label={copied ? t("common.copied") : copyLabel} />}>
        {typeof children === "function" ? children(copied) : (children ?? defaultIcon)}
      </TooltipTrigger>
      <TooltipContent>{copied ? t("common.copied") : (title || copyLabel)}</TooltipContent>
    </Tooltip>
    <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{copied ? t("common.copied") : ""}</span>
    </>
  );
}
