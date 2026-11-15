// Binary size profiler.
//
// Answers "why is the binary this big?" with numbers instead of opinions: it
// attributes the symbol sizes of a Go binary to the modules that own them.
// The question that started this ("the WebRTC stack makes the binary huge")
// is answered by running it instead of arguing.
//
// Usage:
//   node scripts/binary-size.mjs --build [--goos linux] [--goarch amd64]
//   node scripts/binary-size.mjs <binary>
//
// `--build` builds TWO binaries: the stripped one a release ships, and an
// unstripped one, because `go tool nm` needs the symbol table that `-s -w`
// removes. The build is a plain `go build` of ./cmd/server.

import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const MB = 1024 * 1024;

// Two-segment hosts: gopkg.in/yaml.v3, modernc.org/sqlite, gorm.io/gorm,
// google.golang.org/protobuf.
const TWO_SEGMENT_HOSTS = new Set(["gopkg.in", "modernc.org", "gorm.io", "google.golang.org"]);

// Fallback hosts, used when go.mod cannot be read. The real list comes from
// go.mod itself, so a new module host is picked up automatically.
export const DEFAULT_HOSTS = [
  "github.com",
  "golang.org",
  "gopkg.in",
  "modernc.org",
  "gorm.io",
  "go.mongodb.org",
  "google.golang.org",
  "golang.zx2c4.com",
  "software.sslmate.com",
  "go.uber.org",
  "sigs.k8s.io",
  "filippo.io",
  "lukechampine.com",
];

/** The module hosts named by a go.mod file. */
export function moduleHostsFromGoMod(text) {
  const hosts = new Set();
  for (const line of text.split("\n")) {
    const m = line.match(/^\s*([a-z0-9.-]+\.[a-z]{2,})\/\S+/);
    if (m) hosts.add(m[1]);
  }
  return hosts.size > 0 ? hosts : new Set(DEFAULT_HOSTS);
}

/**
 * The module a symbol belongs to.
 *
 * Symbol names come in a few shapes: `github.com/x/y.Func`, `type:.eq.github
 * .com/x/y.T`, `runtime.mallocgc`, `go:func.*`. The module path is found by
 * looking for a known module HOST inside the name (the `type:.eq.` prefix
 * hides it behind a method name), and everything else is the Go runtime and
 * the standard library.
 */
export function attributeSymbol(name, hosts = DEFAULT_HOSTS) {
  let path = name;
  if (path.startsWith("type:.")) path = path.slice("type:.".length);
  else if (path.startsWith("go:") || path.startsWith("runtime.")) return "go runtime + stdlib";
  const seg = path.split("/");
  for (let i = 0; i < seg.length; i++) {
    // An EXACT host wins before the "ends with a host" rule: the segment
    // "google.golang.org" also ends with ".golang.org", and charging protobuf
    // to golang.org/x would be nonsense.
    let host = null;
    for (const h of hosts) {
      if (seg[i] === h) { host = h; break; }
    }
    if (!host) {
      let longest = null;
      for (const h of hosts) {
        if (!seg[i].endsWith("." + h)) continue;
        if (!longest || h.length > longest.length) longest = h;
      }
      host = longest;
    }
    if (!host) continue;
    {
      const depth = TWO_SEGMENT_HOSTS.has(host) ? 2 : 3;
      const els = [host, ...seg.slice(i + 1, i + depth)];
      if (els.length === depth && els.length === seg.length - i) {
        // The symbol hangs off the last package element ("sqlite.Open"). Strip
        // it, but only when it really looks like a symbol: a package element may
        // contain a dot of its own ("yaml.v3").
        els[els.length - 1] = els[els.length - 1].replace(/\.(?=[A-Z_(]).*$/, "");
      }
      return els.join("/");
    }
  }
  return "go runtime + stdlib";
}

/**
 * Parse `go tool nm -size` output into { total, groups }.
 *
 * Lines look like:
 *   4a1b2c3d  1234 T github.com/pion/webrtc/v4.NewPeer
 */
export function parseNmOutput(text, hosts = DEFAULT_HOSTS) {
  const groups = new Map();
  let total = 0;
  for (const line of text.split("\n")) {
    const m = line.match(/^\s*[0-9a-fA-F]+\s+(\d+)\s+(\S+)\s+(.*)$/);
    if (!m) continue;
    const size = Number(m[1]);
    const key = attributeSymbol(m[3].trim(), hosts);
    total += size;
    groups.set(key, (groups.get(key) || 0) + size);
  }
  return { total, groups };
}

/** Render the profile as the report the CLI prints. */
export function formatReport(profile, limit = 15) {
  const mb = (bytes) => (bytes / MB).toFixed(1);
  const rows = [...profile.groups.entries()].sort((a, b) => b[1] - a[1]);
  const lines = [`total symbols: ${mb(profile.total)} MB`];
  for (const [label, size] of rows.slice(0, limit)) {
    lines.push(`${String(mb(size)).padStart(6)} MB  ${label}`);
  }
  const rest = rows.slice(limit).reduce((sum, [, size]) => sum + size, 0);
  if (rest > 0) lines.push(`${String(mb(rest)).padStart(6)} MB  (${rows.length - limit} smaller modules)`);
  return lines.join("\n");
}

function repoRoot() {
  return fileURLToPath(new URL("..", import.meta.url));
}

function knownHosts() {
  const goMod = join(repoRoot(), "go.mod");
  if (!existsSync(goMod)) return new Set(DEFAULT_HOSTS);
  return moduleHostsFromGoMod(readFileSync(goMod, "utf8"));
}

function goBuild(out, goos, goarch, extraLdflags) {
  const env = { ...process.env, GOOS: goos, GOARCH: goarch };
  execFileSync("go", ["build", "-ldflags", extraLdflags, "-o", out, "./cmd/server"], {
    cwd: repoRoot(),
    env,
    stdio: "inherit",
  });
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const args = process.argv.slice(2);
  const build = args.includes("--build");
  const goos = args.includes("--goos") ? args[args.indexOf("--goos") + 1] : "linux";
  const goarch = args.includes("--goarch") ? args[args.indexOf("--goarch") + 1] : "amd64";
  const positional = args.filter((a) => !a.startsWith("--") && a !== goos && a !== goarch);
  const hosts = knownHosts();

  if (build) {
    const dir = mkdtempSync(join(tmpdir(), "forgec2-size-"));
    const stripped = join(dir, `forgec2-${goos}-${goarch}`);
    const symbols = join(dir, `forgec2-${goos}-${goarch}-symbols`);
    const version = "-X github.com/forgec2/forgec2/internal/server.ServerVersion=size-profile";
    console.log(`building ${goos}/${goarch} ...`);
    goBuild(stripped, goos, goarch, `-s -w ${version}`);
    goBuild(symbols, goos, goarch, version);
    console.log(`shipped size (-s -w): ${(statSync(stripped).size / MB).toFixed(1)} MB  ${stripped}`);
    console.log(`unstripped size:      ${(statSync(symbols).size / MB).toFixed(1)} MB  ${symbols}`);
    const out = execFileSync("go", ["tool", "nm", "-size", symbols], { maxBuffer: 1 << 30, encoding: "utf8" });
    console.log(formatReport(parseNmOutput(out, hosts)));
    process.exit(0);
  }

  const binary = positional[0];
  if (!binary) {
    console.error("usage: node scripts/binary-size.mjs [--build] [--goos linux] [--goarch amd64] [<binary>]");
    process.exit(2);
  }
  console.log(`file size: ${(statSync(binary).size / MB).toFixed(1)} MB  ${binary}`);
  const out = execFileSync("go", ["tool", "nm", "-size", binary], { maxBuffer: 1 << 30, encoding: "utf8" });
  console.log(formatReport(parseNmOutput(out, hosts)));
}
