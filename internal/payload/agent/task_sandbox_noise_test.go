//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestNoiseArgsClamped(t *testing.T) {
	dur, workers := noiseArgs("")
	if dur != noiseDefaultDur || workers != 2 {
		t.Fatalf("defaults: %v %d", dur, workers)
	}
	dur, workers = noiseArgs("999:999")
	if dur != noiseMaxDuration || workers != noiseMaxWorkers {
		t.Fatalf("clamp: %v %d", dur, workers)
	}
	dur, workers = noiseArgs("3:2")
	if dur != 3*time.Second || workers != 2 {
		t.Fatalf("parse: %v %d", dur, workers)
	}
	dur, workers = noiseArgs("-1:0")
	if dur <= 0 || workers <= 0 {
		t.Fatalf("non-positive should fall back: %v %d", dur, workers)
	}
}

func TestNoiseBurstBoundedAndTerminates(t *testing.T) {
	start := time.Now()
	total := runNoiseBurst(60*time.Millisecond, 2)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("noise burst did not terminate promptly: %v", elapsed)
	}
	if total < 0 {
		t.Fatalf("negative op count: %d", total)
	}
	if got := runNoiseBurst(0, 2); got != 0 {
		t.Fatalf("zero duration should be a no-op: %d", got)
	}
}

func TestSandboxNoiseHandleBase64(t *testing.T) {
	var res TaskResult
	handleSandboxNoise(Task{Type: "sandbox_noise", Command: "1:1"}, &res)
	if res.Encoding != "base64" || res.Output == "" {
		t.Fatalf("bad envelope: %q %q", res.Encoding, res.Output)
	}
	decoded, err := base64.StdEncoding.DecodeString(res.Output)
	if err != nil || !strings.Contains(string(decoded), "sandbox_noise") {
		t.Fatalf("decoded output = %q err=%v", decoded, err)
	}
}
