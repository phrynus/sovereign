import type { WSMessage } from "@/lib/wsContext";

export function decodeShellOutput(data: string, encoding?: unknown): string {
  if (encoding !== "base64" || !data) return data;
  try {
    const binary = atob(data);
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return data;
  }
}

export function shellWriteCommand(shellID: string, data: string): string {
  return `${shellID}:${data}`;
}

export function isShellOutputFrame(
  msg: WSMessage,
  agentID: string,
  shellID: string | null,
): msg is WSMessage & { type: "shell_output"; shell_id: string; data: string; encoding?: string } {
  return msg.type === "shell_output"
    && msg.agent_id === agentID
    && msg.shell_id === shellID
    && typeof msg.data === "string";
}
