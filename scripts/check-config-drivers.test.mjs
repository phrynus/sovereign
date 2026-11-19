import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { analyzeSource, SOURCES } from "./check-config-drivers.mjs";

test("an unquoted sqlite driver is reported", () => {
  assert.match(analyzeSource("t.yaml", "database:\n  driver: sqlite\n")[0], /t\.yaml:2: driver "sqlite"/);
});

test("a quoted sqlite driver in a heredoc is reported", () => {
  assert.equal(analyzeSource("setup.sh", '  driver: "sqlite"\n').length, 1);
});

test("postgres passes", () => {
  assert.deepEqual(analyzeSource("t.yaml", "database:\n  driver: postgres\n  dsn: ''\n"), []);
});

test("a commented-out legacy example is not an active declaration", () => {
  assert.deepEqual(analyzeSource("t.yaml", "  # driver: sqlite\n  driver: postgres\n"), []);
});

test("a source with no active declaration is reported", () => {
  assert.match(analyzeSource("t.yaml", "# driver: sqlite\n")[0], /no active database.driver/);
});

test("every tracked template and generator declares postgres", () => {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  for (const name of SOURCES) {
    assert.deepEqual(analyzeSource(name, readFileSync(join(root, name), "utf8")), [], name);
  }
});
