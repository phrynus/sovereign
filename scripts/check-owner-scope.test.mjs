import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";

const checker = fileURLToPath(new URL("./check-owner-scope.mjs", import.meta.url));

function runChecker(source) {
  const root = mkdtempSync(join(tmpdir(), "owner-scope-"));
  try {
    const serverDir = join(root, "internal", "server");
    mkdirSync(serverDir, { recursive: true });
    writeFileSync(join(serverDir, "handlers.go"), source);
    return spawnSync(process.execPath, [checker, root], { encoding: "utf8" });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

const cases = [
  { model: "PhishingEvent", scope: "phishingEventScope" },
  { model: "BuildLog", scope: "tenantScope" },
  { model: "MeshPeer", scope: "visibleAgentIDSubquery" },
];

for (const { model, scope } of cases) {
  test(`check-owner-scope requires ${scope} for ${model}`, () => {
    const result = runChecker(`
package server
import (
  "github.com/gin-gonic/gin"
  "github.com/r0si/sovereign/internal/db"
)
func (s *Server) handleGood(c *gin.Context) {
  _ = ${scope}(c)
  var row db.${model}
  s.db.First(&row)
}
func (s *Server) handleBad(c *gin.Context) {
  var row db.${model}
  s.db.First(&row)
}
`);
    assert.equal(result.status, 1, result.stdout + result.stderr);
    assert.match(result.stderr, new RegExp(`handleBad[\\s\\S]*db\\.${model}`));
    assert.doesNotMatch(result.stderr, /handleGood/);
  });
}

test("check-owner-scope accepts relationship-scoped PhishingEvent BuildLog and MeshPeer reads", () => {
  const result = runChecker(`
package server
import (
  "github.com/gin-gonic/gin"
  "github.com/r0si/sovereign/internal/db"
)
func (s *Server) handlePhishing(c *gin.Context) {
  _ = s.phishingEventScope(c)
  var rows []db.PhishingEvent
  s.db.Find(&rows)
}
func (s *Server) handleBuild(c *gin.Context) {
  var rows []db.BuildLog
  s.tenantScope(s.db, c).Find(&rows)
}
func (s *Server) handleMesh(c *gin.Context) {
  sub, ok := s.visibleAgentIDSubquery(c)
  if !ok { return }
  var rows []db.MeshPeer
  s.db.Where("agent_id IN (?) AND peer_id IN (?)", sub, sub).Find(&rows)
}
`);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /check-owner-scope: OK/);
});
