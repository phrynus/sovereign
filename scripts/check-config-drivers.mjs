// Every tracked config template/generator must emit a database driver the
// server actually ships. v2.8 removed SQLite, but scripts/setup-dev.sh kept a
// `driver: sqlite` heredoc: a fresh dev setup produced a config.yaml the server
// refused at boot ("unsupported database driver"), and nothing in `npm run
// check` noticed. The detector is exported so the shape is pinned by a test.
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const SOURCES = [
  "config.example.yaml",
  "scripts/setup-dev.sh",
  "deploy/demo/deploy.sh",
];

// Only active declarations count: a commented-out legacy example is allowed.
const ACTIVE_DRIVER = /^\s*driver:\s*["']?([A-Za-z0-9_-]+)["']?\s*(?:#.*)?$/;

export function analyzeSource(name, source) {
  const findings = [];
  let declarations = 0;
  source.split(/\r?\n/).forEach((line, i) => {
    const match = ACTIVE_DRIVER.exec(line);
    if (!match) return;
    declarations += 1;
    if (match[1].toLowerCase() !== "postgres") {
      findings.push(`${name}:${i + 1}: driver "${match[1]}" is unsupported (only postgres is shipped)`);
    }
  });
  if (declarations === 0) {
    findings.push(`${name}: no active database.driver declaration`);
  }
  return findings;
}

function main() {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const findings = SOURCES.flatMap((name) =>
    analyzeSource(name, readFileSync(join(root, name), "utf8")),
  );
  if (findings.length) {
    console.error(`check-config-drivers FAILED — ${findings.length} finding(s):`);
    for (const finding of findings) console.error(`  ${finding}`);
    process.exit(1);
  }
  console.log(
    `check-config-drivers OK — ${SOURCES.length} tracked config template(s)/generator(s) declare postgres`,
  );
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}
