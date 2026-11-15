// Tests for the binary-size profiler.
//
// The profiler is how the "the binary is too big" question gets answered, so
// its attribution has to be right: a module that is charged to the runtime
// (or to nothing) makes the report useless.
//
// Run: node --test scripts/binary-size.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { attributeSymbol, formatReport, parseNmOutput } from "./binary-size.mjs";

test("a module path is charged to its module", () => {
  assert.equal(attributeSymbol("github.com/pion/webrtc/v4.NewPeer"), "github.com/pion/webrtc");
  assert.equal(attributeSymbol("modernc.org/sqlite.Open"), "modernc.org/sqlite");
  assert.equal(attributeSymbol("gopkg.in/yaml.v3.Marshal"), "gopkg.in/yaml.v3");
  assert.equal(attributeSymbol("golang.org/x/crypto/sha256.New"), "golang.org/x/crypto");
  assert.equal(attributeSymbol("go.mongodb.org/mongo-driver/v2/bson.Marshal"), "go.mongodb.org/mongo-driver/v2");
  // A host must match a whole path segment: "google.golang.org" contains
  // "golang.org", and charging protobuf to golang.org/x would be nonsense.
  assert.equal(
    attributeSymbol("google.golang.org/protobuf/internal/impl.Unmarshal"),
    "google.golang.org/protobuf",
  );
});

test("the runtime and the standard library are not charged to a module", () => {
  assert.equal(attributeSymbol("runtime.mallocgc"), "go runtime + stdlib");
  assert.equal(attributeSymbol("crypto/tls.Conn.Handshake"), "go runtime + stdlib");
  assert.equal(attributeSymbol("go:func.*"), "go runtime + stdlib");
  assert.equal(attributeSymbol("internal/abi.TypeEq"), "go runtime + stdlib");
});

test("a type symbol is attributed like a function symbol", () => {
  assert.equal(
    attributeSymbol("type:.eq.github.com/forgec2/forgec2/internal/server.Task"),
    "github.com/forgec2/forgec2",
  );
});

test("nm output is parsed into per-module totals", () => {
  const nm = [
    "4a1b2c3d  2097152 T github.com/pion/webrtc/v4.NewPeer",
    "4a1b2c3e  1048576 T github.com/pion/webrtc/v4.Dial",
    "5b2c3d4e  3145728 T runtime.mallocgc",
    "not a symbol line",
  ].join("\n");
  const profile = parseNmOutput(nm);
  assert.equal(profile.total, 2097152 + 1048576 + 3145728);
  assert.equal(profile.groups.get("github.com/pion/webrtc"), 2097152 + 1048576);
  assert.equal(profile.groups.get("go runtime + stdlib"), 3145728);
});

test("the report lists the biggest module first and keeps the rest", () => {
  const profile = {
    total: 3 * 1024 * 1024,
    groups: new Map([
      ["small/one", 1024 * 1024],
      ["big/two", 2 * 1024 * 1024],
      ["tiny/three", 512],
    ]),
  };
  const report = formatReport(profile, 1);
  assert.match(report, /total symbols: 3\.0 MB/);
  assert.match(report, /2\.0 MB  big\/two/);
  assert.match(report, /\(2 smaller modules\)/);
});
