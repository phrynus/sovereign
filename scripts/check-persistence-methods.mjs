#!/usr/bin/env node
// Keep the persistence method registry in pkg/protocol in sync with every
// agent-side apply/remove switch. The install/list/remove drift this guards
// against shipped twice: names used by persistence_add were invisible to the
// detection list, and a Windows-only method could fall through to a default
// install on Linux/macOS. Both are user-visible lies, not style issues.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (rel) => fs.readFileSync(path.join(root, rel), "utf8");

const spec = read("pkg/protocol/persistence_methods.go");
const declared = [...spec.matchAll(/^\s*Persistence\w+\s+PersistenceMethod\s+=\s+"([a-z0-9_]+)"/gm)].map((m) => m[1]);
if (declared.length < 10) {
  console.error("check-persistence-methods: could not read the protocol method registry");
  process.exit(2);
}
const dupes = declared.filter((v, i) => declared.indexOf(v) !== i);
if (dupes.length) {
  console.error("check-persistence-methods: duplicate protocol methods: " + [...new Set(dupes)].join(", "));
  process.exit(1);
}

function switchMethods(src) {
  const found = new Set();
  for (const fn of ["applyPersistence", "removePersistence"]) {
    const start = src.indexOf(`func ${fn}(`);
    if (start < 0) continue;
    const open = src.indexOf("{", start);
    let depth = 0;
    let end = -1;
    for (let i = open; i < src.length; i++) {
      if (src[i] === "{") depth++;
      else if (src[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    if (end < 0) continue;
    const body = src.slice(open, end);
    for (const clause of body.matchAll(/case\s+((?:"[a-z0-9_]+"\s*,?\s*)+):/g)) {
      for (const value of clause[1].matchAll(/"([a-z0-9_]+)"/g)) found.add(value[1]);
    }
  }
  return found;
}

const agentFiles = [
  "internal/payload/agent/persist_windows.go",
  "internal/payload/agent/agent_linux.go",
  "internal/payload/agent/agent_darwin.go",
];
const implemented = new Set();
for (const rel of agentFiles) {
  for (const method of switchMethods(read(rel))) implemented.add(method);
}

const missing = declared.filter((m) => !implemented.has(m));
const unknown = [...implemented].filter((m) => !declared.includes(m));
if (missing.length || unknown.length) {
  if (missing.length) console.error("check-persistence-methods: in protocol but no agent switch: " + missing.join(", "));
  if (unknown.length) console.error("check-persistence-methods: agent switch missing from protocol: " + unknown.join(", "));
  process.exit(1);
}
console.log(`OK: ${declared.length} persistence methods match across protocol and agent switches`);
