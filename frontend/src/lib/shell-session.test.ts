import { describe, expect, it } from "vitest";
import { decodeShellOutput, isShellOutputFrame, shellWriteCommand } from "@/lib/shell-session";

describe("interactive shell wire helpers", () => {
  it("decodes base64 UTF-8 output without corrupting multibyte text", () => {
    const encoded = btoa(String.fromCharCode(...new TextEncoder().encode("提示符> 你好\n")));
    expect(decodeShellOutput(encoded, "base64")).toBe("提示符> 你好\n");
  });

  it("passes plain output through and does not crash on malformed base64", () => {
    expect(decodeShellOutput("plain", "")).toBe("plain");
    expect(decodeShellOutput("not base64!", "base64")).toBe("not base64!");
  });

  it("binds input to the shell session while preserving colons in data", () => {
    expect(shellWriteCommand("sh_7", "echo https://example.test\r")).toBe("sh_7:echo https://example.test\r");
  });

  it("accepts only output frames for the active agent and shell", () => {
    const frame = { type: "shell_output", agent_id: "a1", shell_id: "sh_7", data: "b2s=", encoding: "base64" };
    expect(isShellOutputFrame(frame, "a1", "sh_7")).toBe(true);
    expect(isShellOutputFrame(frame, "a1", null)).toBe(false);
    expect(isShellOutputFrame(frame, "a2", "sh_7")).toBe(false);
    expect(isShellOutputFrame({ ...frame, type: "task_update" }, "a1", "sh_7")).toBe(false);
  });
});
