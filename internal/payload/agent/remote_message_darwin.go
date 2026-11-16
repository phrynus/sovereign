//go:build darwin
// +build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// showRemoteMessage displays an operator message with System Events. The text
// is escaped for the AppleScript string literal (never concatenated into a
// shell command), and the dialog blocks until dismissed.
func showRemoteMessage(title, text string) error {
	escape := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	script := fmt.Sprintf(
		`display dialog "%s" with title "%s" buttons {"OK"} default button "OK" with icon note`,
		escape(text), escape(title))
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
