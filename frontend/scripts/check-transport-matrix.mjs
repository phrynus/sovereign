// Fails when the operator-facing transport quality labels drift from the
// capability matrix. The UI (src/pages/payload-generator/components/
// transport-quality.ts) decides which transports are hidden as "experimental",
// while docs/CAPABILITY_MATRIX.md is the published source of truth. A mismatch
// once shipped: gRPC/SSH/ICMP/UDP/QUIC were Hardened in the docs but marked
// Experimental in the UI, so an operator saw "experimental" on five production
// transports. Neither check:docs nor any other gate saw it because the
// transport table is hand-written (outside the generated markers).
//
// This guard compares the quality of every transport that appears in BOTH the
// UI list and the docs table. Transports that exist only in one are reported
// too (a transport shipped in the UI but absent from the matrix, or vice versa)
// except for a small, explicit alias/exclusion set documented below.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const DOCS = join(root, "..", "docs", "CAPABILITY_MATRIX.md");
const UI = join(root, "src", "pages", "payload-generator", "components", "transport-quality.ts");

function fail(msg) {
  console.error(`check-transport-matrix: ${msg}`);
  process.exit(1);
}

// Map docs transport names to UI values. A combined docs row maps to several.
const DOC_TO_UI = {
  "HTTP(S)": ["http"],
  TCP: ["tcp"],
  DNS: ["dns"],
  WSS: ["wss"],
  gRPC: ["grpc"],
  SSH: ["ssh"],
  ICMP: ["icmp"],
  UDP: ["udp"],
  QUIC: ["quic"],
  "mTLS / h2c": ["mtls", "h2c"],
  // SMB P2P is a P2P mode, not a beacon transport in BEACON_TRANSPORTS.
  "SMB P2P": [],
};

try {
  const docs = readFileSync(DOCS, "utf8");
  const ui = readFileSync(UI, "utf8");

  // Parse docs transport rows: | Name | ... | Quality |
  const docQuality = new Map();
  for (const line of docs.split("\n")) {
    const m = line.match(/^\|\s*([^|]+?)\s*\|.*\|\s*(Core|Hardened|Scripted|Experimental|Stub)\b/);
    if (m && DOC_TO_UI[m[1]]) {
      for (const v of DOC_TO_UI[m[1]]) docQuality.set(v, m[2].toLowerCase());
    }
  }
  if (docQuality.size === 0) fail("no transport rows parsed from CAPABILITY_MATRIX.md — table shape changed?");

  // Parse UI rows: { value: "x", label: "...", quality: "y" }
  const uiQuality = new Map();
  for (const m of ui.matchAll(/\{\s*value:\s*"([^"]+)",\s*label:\s*"[^"]*",\s*quality:\s*"(core|hardened|experimental)"/g)) {
    uiQuality.set(m[1], m[2]);
  }
  if (uiQuality.size === 0) fail("no transports parsed from transport-quality.ts — object shape changed?");

  const mismatches = [];
  for (const [value, docQ] of docQuality) {
    const uiQ = uiQuality.get(value);
    if (!uiQ) {
      mismatches.push(`${value}: in docs as ${docQ} but missing from UI transports`);
    } else if (uiQ !== docQ) {
      mismatches.push(`${value}: docs=${docQ} UI=${uiQ}`);
    }
  }
  for (const [value] of uiQuality) {
    if (!docQuality.has(value)) mismatches.push(`${value}: in UI but missing from docs transport table`);
  }

  if (mismatches.length) {
    fail(`transport quality drift:\n  ${mismatches.join("\n  ")}\nUpdate transport-quality.ts (and its test) or the CAPABILITY_MATRIX.md transport table so they agree.`);
  }

  console.log(`check-transport-matrix: ${uiQuality.size} UI transports match the capability matrix.`);
} catch (err) {
  fail(`could not read inputs: ${err.message}`);
}
