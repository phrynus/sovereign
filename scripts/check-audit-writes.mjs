#!/usr/bin/env node
// Every audit row must go through persistAuditBatch: that is the single place
// that assigns PrevHash/EntryHash, signs rows when keying is enabled and
// appends the external anchor. A direct Create/Save anywhere else silently
// produces an unsigned, unanchored row that looks like part of the chain.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const canonical = path.join(root, "internal", "server", "audit.go");
const source = fs.readFileSync(canonical, "utf8");
for (const marker of ["SignRow(", "VerifyRow(", "appendAuditAnchor("]) {
  if (!source.includes(marker)) {
    console.error(`check-audit-writes: the canonical writer lost ${marker}`);
    process.exit(1);
  }
}

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

const offenders = [];
for (const file of walk(path.join(root, "internal"))) {
  if (file === canonical) continue;
  const rel = path.relative(root, file).replaceAll("\\", "/");
  const text = fs.readFileSync(file, "utf8");
  for (const line of text.split("\n")) {
    // Keep the pattern narrow: archive writers also call Create("audit.ndjson").
    if (/\.(Create|Save|CreateInBatches)\(/.test(line) && /AuditLog/.test(line)) {
      offenders.push(`${rel}: ${line.trim()}`);
    }
  }
}
if (offenders.length) {
  console.error("check-audit-writes: audit rows bypass persistAuditBatch:");
  for (const line of offenders) console.error("  " + line);
  process.exit(1);
}
console.log("OK: audit writes go through the hashing/signing writer");
