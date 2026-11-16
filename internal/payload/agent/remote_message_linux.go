//go:build linux
// +build linux

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// showRemoteMessage displays an operator message through the desktop's own
// notification tool. Arguments are passed as argv (never a shell), so the text
// cannot become a command. zenity blocks until dismissed; notify-send does not.
func showRemoteMessage(title, text string) error {
	type attempt struct {
		bin  string
		args []string
	}
	attempts := []attempt{
		{"zenity", []string{"--info", "--no-wrap", "--title", title, "--text", text}},
		{"kdialog", []string{"--title", title, "--msgbox", text}},
		{"xmessage", []string{"-center", title + ": " + text}},
		{"notify-send", []string{title, text}},
	}
	var lastErr error
	for _, a := range attempts {
		if _, err := exec.LookPath(a.bin); err != nil {
			lastErr = err
			continue
		}
		out, err := exec.Command(a.bin, a.args...).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %v (%s)", a.bin, err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("no dialog tool available: %v", lastErr)
}
