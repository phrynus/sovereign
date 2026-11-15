#!/usr/bin/env node
/**
 * The Go toolchain in the Dockerfile must equal go.mod's `toolchain` line.
 *
 * Why this exists: Dependabot groups the Docker base images and bumped
 * golang:1.26.11-alpine to golang:1.27.0-alpine (#53). The image's builder is
 * where the shipped binary's standard library comes from, so that bump
 * reintroduced GO-2026-6859 - the advisory that made the module pin 1.26.11 in
 * the first place, and which is fixed in 1.26.11 and 1.27.**4**, not in
 * 1.27.0-1.27.3. Every job stayed green because govulncheck scans the module
 * with the CI toolchain, not the Docker base image: the drift is invisible to
 * every other check.
 *
 * Reads the first `golang:<version>-...` FROM line and the `toolchain go<version>`
 * directive. Exits non-zero on a mismatch, on a missing directive, or when the
 * pattern cannot be found (a check that silently reads nothing guards nothing).
 *
 * --self-test exercises the parser against fixtures without touching the repo.
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

/** Extract the Go version from the Dockerfile's builder stage, e.g. "1.26.11". */
export function dockerfileGoVersion(dockerfile) {
  const match = dockerfile.match(/^\s*FROM\s+[^\n]*?\bgolang:(\d+\.\d+\.\d+)/m);
  return match ? match[1] : null;
}

/** Extract go.mod's toolchain version, e.g. "1.26.11". */
export function goModToolchain(gomod) {
  const match = gomod.match(/^\s*toolchain\s+go(\d+\.\d+\.\d+)\s*$/m);
  return match ? match[1] : null;
}

export function compare(dockerfileVersion, gomodVersion) {
  if (!dockerfileVersion) return "the Dockerfile has no golang:<x.y.z> builder image";
  if (!gomodVersion) return "go.mod has no `toolchain go<x.y.z>` directive";
  if (dockerfileVersion !== gomodVersion) {
    return `the Dockerfile builds with go${dockerfileVersion} but go.mod pins ${gomodVersion}: ` +
      "the stdlib is linked into the shipped binary, so the builder decides which CVEs ship";
  }
  return null;
}

function selfTest() {
  const cases = [
    { name: "match", df: "FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:aa AS b\n", gm: "go 1.26.0\n\ntoolchain go1.26.11\n", want: null },
    { name: "drift", df: "FROM golang:1.27.0-alpine AS b\n", gm: "toolchain go1.26.11\n", want: "1.27.0" },
    { name: "no image", df: "FROM alpine:3.22\n", gm: "toolchain go1.26.11\n", want: "no golang:" },
    { name: "no directive", df: "FROM golang:1.26.11-alpine AS b\n", gm: "go 1.26.0\n", want: "no `toolchain" },
  ];
  let failed = 0;
  for (const c of cases) {
    const dfv = dockerfileGoVersion(c.df);
    const gmv = goModToolchain(c.gm);
    const problem = compare(dfv, gmv);
    const ok = c.want === null ? problem === null : problem !== null && problem.includes(c.want);
    if (!ok) {
      failed += 1;
      console.error(`FAIL ${c.name}: dockerfile=${dfv} gomod=${gmv} problem=${problem}`);
    }
  }
  if (failed > 0) {
    console.error(`self-test failed (${failed}/${cases.length})`);
    process.exit(1);
  }
  console.log(`self-test OK (${cases.length} cases)`);
}

if (process.argv.includes("--self-test")) {
  selfTest();
} else {
  const dockerfile = readFileSync(join(ROOT, "Dockerfile"), "utf8");
  const gomod = readFileSync(join(ROOT, "go.mod"), "utf8");
  const problem = compare(dockerfileGoVersion(dockerfile), goModToolchain(gomod));
  if (problem) {
    console.error(`check-docker-go-version: ${problem}`);
    process.exit(1);
  }
  console.log(`check-docker-go-version: OK (go${goModToolchain(gomod)} in the Dockerfile and go.mod)`);
}
