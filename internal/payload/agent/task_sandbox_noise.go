//go:build linux || windows || darwin
// +build linux windows darwin

package main

// sandbox_noise implements the active anti-sandbox "noise worker" technique
// seen in layered stealers (火绒 2060): short-lived, bounded CPU / temp-file /
// process churn designed to make a sandbox's event heuristics noisy and slow
// dynamic analysis. It is deliberately opt-in (an operator must queue the task)
// and hard-bounded — unbounded noise would be a self-inflicted denial of
// service on a real host, which is exactly what this product must not do.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	noiseMaxWorkers  = 4
	noiseMaxDuration = 10 * time.Second
	noiseDefaultDur  = 2 * time.Second
)

// noiseArgs parses "<seconds>:<workers>" from the task command with safe
// clamping. Empty means the defaults; both values are bounded so an operator
// typo can never turn this into a long, high-CPU burn.
func noiseArgs(command string) (time.Duration, int) {
	dur, workers := noiseDefaultDur, 2
	command = strings.TrimSpace(command)
	if command == "" {
		return dur, workers
	}
	parts := strings.Split(command, ":")
	if len(parts) > 0 {
		if s, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && s > 0 {
			d := time.Duration(s) * time.Second
			if d > noiseMaxDuration {
				d = noiseMaxDuration
			}
			dur = d
		}
	}
	if len(parts) > 1 {
		if w, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && w > 0 {
			if w > noiseMaxWorkers {
				w = noiseMaxWorkers
			}
			workers = w
		}
	}
	return dur, workers
}

// noiseOnce performs one bounded unit of churn: a short CPU loop plus a temp
// file create/write/remove in the OS temp dir. It uses the process temp dir
// (not a relative path) and always cleans up its own file.
func noiseOnce() int {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// CPU churn: a small deterministic hash mix over the random seed.
	h := uint64(0)
	for _, v := range b {
		h = h*1099511628211 ^ uint64(v)
		for i := 0; i < 256; i++ {
			h ^= h >> 13
		}
	}
	// Filesystem churn in the OS temp directory, removed on all paths.
	f, err := os.CreateTemp(os.TempDir(), "fc2noise-*")
	if err == nil {
		name := f.Name()
		_, _ = f.Write(b)
		_ = f.Close()
		_ = os.Remove(name)
	}
	return int(h & 0xff)
}

// runNoiseBurst launches `workers` goroutines that loop noiseOnce() until the
// deadline, then waits for all of them. The WaitGroup guarantees no worker
// outlives the task (no leaked goroutines, no post-task resource churn).
func runNoiseBurst(dur time.Duration, workers int) int {
	if dur <= 0 || workers <= 0 {
		return 0
	}
	deadline := time.Now().Add(dur)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			local := 0
			for time.Now().Before(deadline) {
				local += noiseOnce()
			}
			mu.Lock()
			total += local
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	return total
}

func handleSandboxNoise(task Task, res *TaskResult) {
	dur, workers := noiseArgs(task.Command)
	total := runNoiseBurst(dur, workers)
	out := fmt.Sprintf("sandbox_noise: %s, %d workers, %d ops (GOOS=%s)", dur, workers, total, runtime.GOOS)
	res.Output = base64.StdEncoding.EncodeToString([]byte(out))
	res.Encoding = "base64"
}
