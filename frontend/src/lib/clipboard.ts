/**
 * Shared clipboard writer. Uses the async Clipboard API when available and
 * falls back to a hidden textarea + execCommand("copy") on insecure contexts
 * (HTTP), where navigator.clipboard is absent.
 *
 * Returns a boolean synchronously when the fallback path is taken (so callers
 * like <CopyButton> can update feedback in the same tick), or a Promise<boolean>
 * on the async API path. `await copyText(...)` works for both.
 */
function execCopy(text: string): boolean {
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.left = "-9999px";
    const focused = document.activeElement;
    document.body.appendChild(ta);
    try {
      ta.select();
      if (!document.execCommand("copy")) return false;
    } finally {
      ta.remove();
      if (focused instanceof HTMLElement && focused.isConnected) focused.focus();
    }
    return true;
  } catch {
    return false;
  }
}

/** Always-Promise form for callers that chain `.then()`. */
export function copyTextAsync(text: string): Promise<boolean> {
  return Promise.resolve(copyText(text));
}

export function copyText(text: string): boolean | Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      return navigator.clipboard.writeText(text)
        .then(() => true)
        .catch(() => execCopy(text)); // permission denied → try the fallback
    } catch {
      // Some implementations throw before returning a Promise.
      return execCopy(text);
    }
  }
  return execCopy(text);
}
