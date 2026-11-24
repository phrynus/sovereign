export function isLootUrlFetch(command?: string): boolean {
  return /^https?:\/\//i.test(command || "");
}

/** Basename for a completed implant→server pull. Command is the remote path. */
export function lootExfilFilename(task: { command?: string; result?: string; filename?: string }): string | null {
  if (task.filename) return task.filename;
  const cmd = task.command || "";
  if (isLootUrlFetch(cmd)) return null;
  const fromPath = cmd.split(/[\\/]/).filter(Boolean).pop() || "";
  if (fromPath) return fromPath;
  const m = (task.result || "").match(/:\s*([^\s]+) offset /i);
  return m?.[1] || null;
}

export function lootExfilBytes(task: { result?: string; size?: number }): number | null {
  if (typeof task.size === "number" && Number.isFinite(task.size)) return task.size;
  const m = (task.result || "").match(/\((\d+)\s+bytes?\)/i);
  if (!m) return null;
  const n = Number(m[1]);
  return Number.isFinite(n) ? n : null;
}

const DONE: Record<string, true> = { completed: true, success: true, done: true };

export function canDownloadLootExfil(task: { status?: string; command?: string; result?: string; transfer_id?: number }): boolean {
  return DONE[String(task.status || "").toLowerCase()] === true && !!task.transfer_id && lootExfilFilename(task) != null;
}
