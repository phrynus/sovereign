import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchTaskSpecs, formatCompletionList, getCompletions, type TaskSpecInfo } from "./completions";
import { api } from "./api";
import { paths } from "./api-paths";

describe("getCompletions", () => {
  it("returns the first 20 windows commands for empty input", () => {
    const out = getCompletions("", "windows");
    expect(out).toHaveLength(20);
    expect(out[0]).toBe("whoami");
  });

  it("treats whitespace-only input as empty", () => {
    expect(getCompletions("   ", "windows")).toEqual(getCompletions("", "windows"));
  });

  it("uses the linux catalogue only for linux agents", () => {
    expect(getCompletions("ipconfig", "linux")).toEqual([]);
    expect(getCompletions("ipconfig", "windows")).toEqual(["ipconfig", "ipconfig /all"]);
    expect(getCompletions("id", "linux")).toEqual(["id"]);
    // Any other OS string falls back to the windows catalogue.
    expect(getCompletions("mimikatz", "darwin")).toEqual(["mimikatz"]);
  });

  it("matches prefixes case-insensitively and keeps only matching commands", () => {
    expect(getCompletions("WHO", "windows")).toEqual(["whoami"]);
    expect(getCompletions("get-e", "windows")).toEqual(["Get-ExecutionPolicy"]);
    expect(getCompletions("ls", "linux")).toEqual(["ls -la", "ls -la /tmp"]);
  });

  it("filters the subcommands of the matched base command", () => {
    expect(getCompletions("netstat -a", "windows")).toEqual(["-ano", "-anob"]);
    expect(getCompletions("netstat -p", "windows")).toEqual(["-p tcp", "-p udp"]);
    expect(getCompletions("reg q", "windows")).toEqual(["query"]);
    expect(getCompletions("ipconfig /", "windows")).toEqual(["/all", "/release", "/renew", "/flushdns"]);
    // The base command lookup is case-insensitive too.
    expect(getCompletions("NETSTAT -A", "windows")).toEqual(["-ano", "-anob"]);
  });

  it("joins multi-part prefixes when filtering subcommands", () => {
    expect(getCompletions("curl -x post", "linux")).toEqual(["-X POST"]);
    expect(getCompletions("powershell -c", "windows")).toEqual(["-Command"]);
    expect(getCompletions("powershell -e", "windows")).toEqual(["-EncodedCommand"]);
    expect(getCompletions("powershell -", "windows")).toEqual(["-Command", "-EncodedCommand", "-File", "-NoProfile"]);
  });

  it("falls back to command-prefix matching for bases without subcommands", () => {
    expect(getCompletions("net u", "windows")).toEqual(["net user", "net use"]);
    expect(getCompletions("net user", "windows")).toEqual(["net user"]);
  });

  it("does not leak subcommands into bare one-word prefixes", () => {
    // A single token never enters the subcommand branch, even for bases that have them.
    expect(getCompletions("reg", "windows")).toEqual(["reg query", "reg add", "reg delete"]);
    expect(getCompletions("dir", "windows")).toEqual(["dir"]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(getCompletions("zzz", "windows")).toEqual([]);
    expect(getCompletions("netstat -z", "windows")).toEqual([]);
    expect(getCompletions("net zzz", "windows")).toEqual([]);
  });
});

describe("fetchTaskSpecs", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("unwraps the envelope and keeps only specs with a non-empty string type", async () => {
    // Mock at the fetch level so api.get really unwraps {success, data: ...}.
    const requested: string[] = [];
    const fetchMock = vi.fn((...args: Parameters<typeof fetch>) => {
      requested.push(String(args[0]));
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => ({
          success: true,
          data: {
            specs: [
              { type: "keep_awake", name: "Keep Awake", description: "Prevent sleep" },
              { type: "", description: "no type" },
              { name: "missing type" },
              { type: 42 },
            ],
          },
        }),
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const specs = await fetchTaskSpecs();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requested[0]).toContain(paths.tasks.specs);
    expect(specs).toEqual([{ type: "keep_awake", name: "Keep Awake", description: "Prevent sleep" }]);
  });

  it("returns an empty list when the metadata fetch fails", async () => {
    vi.spyOn(api, "get").mockRejectedValue(new Error("offline"));
    await expect(fetchTaskSpecs()).resolves.toEqual([]);
  });
});

describe("getCompletions with task specs", () => {
  const specs: TaskSpecInfo[] = [
    { type: "keep_awake", description: "Prevent the agent from sleeping" },
    { type: "wechat_history", description: "Dump WeChat history" },
  ];

  it("offers spec-only types", () => {
    expect(getCompletions("wechat", "windows", specs)).toEqual(["wechat_history"]);
  });

  it("does not duplicate a static command that the catalog also declares", () => {
    const out = getCompletions("keep", "windows", specs);
    expect(out.filter((item) => item === "keep_awake")).toEqual(["keep_awake"]);
  });

  it("leaves the static catalogue unchanged when no specs are supplied", () => {
    expect(getCompletions("keep", "windows")).toEqual(["keep_awake"]);
    expect(getCompletions("", "windows")).toHaveLength(20);
  });
});

describe("formatCompletionList", () => {
  it("keeps the legacy bare join when no specs are supplied", () => {
    expect(formatCompletionList(["a", "b"], [])).toBe("a  b");
  });

  it("appends a description for a matching spec and truncates long ones", () => {
    const long = "x".repeat(120);
    const specs: TaskSpecInfo[] = [
      { type: "persistence_add", description: "Install persistence mechanism" },
      { type: "long_task", description: long },
    ];
    expect(formatCompletionList(["persistence_add", "long_task", "bare"], specs)).toBe(
      `persistence_add — Install persistence mechanism  long_task — ${"x".repeat(100)}…  bare`,
    );
  });

  it("caps the list at 12 entries plus a remaining-count token", () => {
    const specs: TaskSpecInfo[] = Array.from({ length: 15 }, (_, i) => ({
      type: `task_${i}`,
      description: "desc",
    }));
    const out = formatCompletionList(specs.map(s => s.type), specs);
    expect(out.split("  ")).toHaveLength(13);
    expect(out.split("  ")[12]).toBe("+3 more");
  });
});
