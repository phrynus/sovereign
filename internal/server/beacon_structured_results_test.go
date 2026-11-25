package server

import (
	"strings"
	"testing"
	"time"

	"github.com/r0si/sovereign/internal/crypto"
	"github.com/r0si/sovereign/internal/db"
)

func seedStructuredBeaconTask(t *testing.T, s *Server, agentID string, tenantID uint, taskType, command, status string) (db.Implant, db.Task) {
	t.Helper()
	agent := db.Implant{ID: agentID, TenantID: tenantID, Hostname: "STRUCTURED-" + agentID}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	task := db.Task{AgentID: agentID, TenantID: tenantID, Type: taskType, Command: command, Status: status}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return agent, task
}

func TestBeaconPortscanStructuredIngestIsAuthenticatedAndFinal(t *testing.T) {
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	s, database := newBeaconFinalityTestServer(t)
	agent, task := seedStructuredBeaconTask(t, s, "scan-auth", 1, "portscan", "10.0.0.5:22,80", "running")
	task.Shell = "tcp_syn"
	if err := database.Model(&db.Task{}).Where("id = ?", task.ID).Update("shell", task.Shell).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	result := taskResult{
		TaskID: task.ID, Type: "portscan", ResultID: "scan-auth-1",
		Output: "10.0.0.5:22 open\n10.0.0.5:80 closed\n10.9.9.9:443 open\nmalformed\n",
	}
	s.processTaskResults(agent, []taskResult{result}, agent.ID, now)

	var stored db.Task
	if err := database.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" {
		t.Fatalf("task status=%q error=%q, want completed", stored.Status, stored.Error)
	}
	if stored.Result != result.Output {
		t.Fatalf("stored result=%q, want %q", stored.Result, result.Output)
	}

	var rows []db.ScanResult
	if err := database.Order("port asc").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("scan rows=%d, want 2 authentic targets: %+v", len(rows), rows)
	}
	if rows[0].TargetIP != "10.0.0.5" || rows[0].Port != 22 || rows[0].Protocol != "tcp" || rows[0].State != "open" {
		t.Fatalf("first scan row=%+v", rows[0])
	}
	if rows[1].Port != 80 || rows[1].State != "closed" {
		t.Fatalf("second scan row=%+v", rows[1])
	}

	// A replay, even with a fresh rid, cannot append scan rows after finality.
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "portscan", ResultID: "scan-auth-replay", Output: "10.0.0.5:22 open\n",
	}}, agent.ID, now)
	var count int64
	if err := database.Model(&db.ScanResult{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("terminal replay changed scan rows: %d", count)
	}
}

func TestBeaconPortscanLegacyStructuredPayloadStillIngests(t *testing.T) {
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	s, database := newBeaconFinalityTestServer(t)
	agent, task := seedStructuredBeaconTask(t, s, "scan-json", 1, "portscan", "10.0.0.6:443", "running")
	s.processTaskResults(agent, []taskResult{{
		TaskID: task.ID, Type: "portscan", ResultID: "scan-json-1",
		Output: `{"results":["443:tcp:open:https:1.1:banner:with:colons"]}`,
	}}, agent.ID, time.Now())
	var row db.ScanResult
	if err := database.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.TargetIP != "10.0.0.6" || row.Port != 443 || row.Service != "https" || row.Version != "1.1" || row.Banner != "banner:with:colons" {
		t.Fatalf("structured scan row=%+v", row)
	}
}

func TestBeaconStructuredIngestRejectsWrongAgentTypeAndTerminalTask(t *testing.T) {
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	for _, tc := range []struct {
		name       string
		wrongAgent bool
		wrongType  bool
		terminal   bool
	}{
		{name: "wrong agent", wrongAgent: true},
		{name: "wrong result type", wrongType: true},
		{name: "terminal task", terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, database := newBeaconFinalityTestServer(t)
			status := "running"
			if tc.terminal {
				status = "completed"
			}
			agent, task := seedStructuredBeaconTask(t, s, "scan-guard", 1, "portscan", "10.0.0.7:443", status)
			caller := agent
			if tc.wrongAgent {
				caller = db.Implant{ID: "scan-other", TenantID: 1}
			}
			resultType := "portscan"
			if tc.wrongType {
				resultType = "browser_steal"
			}
			s.processTaskResults(caller, []taskResult{{
				TaskID: task.ID, Type: resultType, ResultID: "scan-guard-1",
				Output: `{"results":["443:tcp:open:https"]}`,
			}}, caller.ID, time.Now())
			var scans int64
			if err := database.Model(&db.ScanResult{}).Count(&scans).Error; err != nil {
				t.Fatal(err)
			}
			var credentials int64
			if err := database.Model(&db.CredentialEntry{}).Count(&credentials).Error; err != nil {
				t.Fatal(err)
			}
			if scans != 0 || credentials != 0 {
				t.Fatalf("unauthenticated structured side effect landed: scans=%d credentials=%d", scans, credentials)
			}
			if tc.wrongAgent || tc.terminal {
				var stored db.Task
				if err := database.First(&stored, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != status {
					t.Fatalf("guarded task status=%q, want %q", stored.Status, status)
				}
			}
		})
	}
}

func TestBeaconLateralResultRecordsCanonicalHostOnlyOnSuccess(t *testing.T) {
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	for _, tc := range []struct {
		name    string
		command string
		err     string
		want    bool
	}{
		{name: "success", command: "wmi|10.0.0.20|alice|secret|whoami", want: true},
		{name: "failed", command: "winrm|10.0.0.21|alice|secret|whoami", err: "access denied"},
		{name: "non canonical", command: `{"method":"wmi","target":"10.0.0.22"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, database := newBeaconFinalityTestServer(t)
			agent, task := seedStructuredBeaconTask(t, s, "lateral-result", 1, "lateral", tc.command, "running")
			s.processTaskResults(agent, []taskResult{{
				TaskID: task.ID, Type: "lateral", ResultID: "lateral-result-1", Output: "lateral output", Error: tc.err,
			}}, agent.ID, time.Now())
			var hosts []db.NetworkHost
			if err := database.Find(&hosts).Error; err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if len(hosts) != 1 || hosts[0].IP != "10.0.0.20" || !strings.Contains(hosts[0].Services, `"method":"wmi"`) {
					t.Fatalf("lateral host=%+v, want canonical wmi target", hosts)
				}
			} else if len(hosts) != 0 {
				t.Fatalf("failed/non-canonical lateral result created hosts: %+v", hosts)
			}
		})
	}
}

func TestBeaconBrowserAndWifiTextFormatsPopulateVault(t *testing.T) {
	crypto.InitLootEncryption(tenantVisibilityMasterHex)
	s, database := newBeaconFinalityTestServer(t)

	browserAgent, browserTask := seedStructuredBeaconTask(t, s, "browser-text", 1, "browser_steal", "all", "running")
	s.processTaskResults(browserAgent, []taskResult{{
		TaskID: browserTask.ID, Type: "browser_steal", ResultID: "browser-text-1",
		Output: "=== Chrome Passwords ===\nURL: https://example.test\nUser: alice\nPass: browser:secret\n---\n",
	}}, browserAgent.ID, time.Now())

	wifiAgent, wifiTask := seedStructuredBeaconTask(t, s, "wifi-text", 1, "wifi_creds", "wifi_creds:all", "running")
	s.processTaskResults(wifiAgent, []taskResult{{
		TaskID: wifiTask.ID, Type: "wifi_creds", ResultID: "wifi-text-1",
		Output: "=== WiFi Credentials ===\n--- Profile: CorpNet ---\nKey Content : wifi:secret\n",
	}}, wifiAgent.ID, time.Now())

	var browser db.CredentialEntry
	if err := database.Where("source = ?", "browser").First(&browser).Error; err != nil {
		t.Fatal(err)
	}
	if note := decryptCredNotes(browser.Notes); !strings.Contains(note, "browser|https://example.test|alice|browser:secret|") {
		t.Fatalf("browser note=%q", note)
	}
	var wifi db.CredentialEntry
	if err := database.Where("source = ?", "wifi").First(&wifi).Error; err != nil {
		t.Fatal(err)
	}
	if wifi.Username != "CorpNet" || wifi.Password != "wifi:secret" || wifi.TenantID != 1 {
		t.Fatalf("wifi credential=%+v", wifi)
	}
}
