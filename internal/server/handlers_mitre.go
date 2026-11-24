package server

import (
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/r0si/sovereign/internal/db"
)

// attackTechnique is the MITRE ATT&CK technique mapping used by the coverage page.
type attackTechnique struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Tactic    string   `json:"tactic"`
	TaskTypes []string `json:"task_types"`
}

// attackTacticMap maps each MITRE tactic to representative techniques and the
// Sovereign task types that satisfy them.
//
// IMPORTANT (batch-5 honesty): every entry in TaskTypes MUST be a task type the
// Go implant actually executes (see internal/payload/agent/task_registry.go).
// Earlier versions advertised fantasy names (exec, schtasks_add, reg_add,
// steal_token, clear_logs, beacon, sleep, ...) which no agent handler
// implements, making techniques appear "covered" by operations nobody could
// run. Techniques with no honest mapping keep an empty list: the UI renders
// them as uncovered rather than claiming a capability.
var attackTacticMap = []struct {
	Tactic     string
	Techniques []attackTechnique
}{
	{
		Tactic: "Execution",
		Techniques: []attackTechnique{
			{ID: "T1059", Name: "Command and Scripting Interpreter", Tactic: "Execution", TaskTypes: []string{"shell", "powerpick", "execute_assembly", "clr_powershell", "bof"}},
			{ID: "T1053", Name: "Scheduled Task/Job", Tactic: "Execution", TaskTypes: []string{}},
			{ID: "T1569", Name: "System Services", Tactic: "Execution", TaskTypes: []string{"services", "killproc"}},
		},
	},
	{
		Tactic: "Persistence",
		Techniques: []attackTechnique{
			{ID: "T1547", Name: "Boot or Logon Autostart", Tactic: "Persistence", TaskTypes: []string{"persistence_add", "persistence_list", "reg_set"}},
			{ID: "T1053", Name: "Scheduled Task/Job", Tactic: "Persistence", TaskTypes: []string{}},
			{ID: "T1543", Name: "Create or Modify System Process", Tactic: "Persistence", TaskTypes: []string{"persistence_add"}},
		},
	},
	{
		Tactic: "Privilege Escalation",
		Techniques: []attackTechnique{
			{ID: "T1134", Name: "Access Token Manipulation", Tactic: "Privilege Escalation", TaskTypes: []string{"elevate", "token_steal", "token_make", "uac_bypass"}},
			{ID: "T1547", Name: "Boot or Logon Autostart", Tactic: "Privilege Escalation", TaskTypes: []string{"persistence_add"}},
			{ID: "T1068", Name: "Exploitation for Privilege Escalation", Tactic: "Privilege Escalation", TaskTypes: []string{"elevate_printnightmare"}},
		},
	},
	{
		Tactic: "Defense Evasion",
		Techniques: []attackTechnique{
			{ID: "T1562", Name: "Impair Defenses", Tactic: "Defense Evasion", TaskTypes: []string{"kill_av", "killproc", "amsi_bypass", "etw_bypass", "unhook_ntdll", "blockdlls"}},
			{ID: "T1070", Name: "Indicator Removal", Tactic: "Defense Evasion", TaskTypes: []string{"log_wipe", "track_wipe"}},
			{ID: "T1055", Name: "Process Injection", Tactic: "Defense Evasion", TaskTypes: []string{"inject", "spawn", "shinject", "shspawn", "migrate", "reflectdll_inject"}},
			{ID: "T1547", Name: "Masquerading", Tactic: "Defense Evasion", TaskTypes: []string{}},
		},
	},
	{
		Tactic: "Credential Access",
		Techniques: []attackTechnique{
			{ID: "T1003", Name: "OS Credential Dumping", Tactic: "Credential Access", TaskTypes: []string{"mimikatz", "creds", "cert_store_list", "browser_steal", "vpn_creds", "wifi_creds"}},
			{ID: "T1056", Name: "Input Capture", Tactic: "Credential Access", TaskTypes: []string{"keylogger_start", "clipboard_get", "webcam", "mic"}},
			{ID: "T1110", Name: "Brute Force", Tactic: "Credential Access", TaskTypes: []string{"password_spray", "cred_check"}},
		},
	},
	{
		Tactic: "Discovery",
		Techniques: []attackTechnique{
			{ID: "T1057", Name: "Process Discovery", Tactic: "Discovery", TaskTypes: []string{"ps", "process_tree"}},
			{ID: "T1083", Name: "File and Directory Discovery", Tactic: "Discovery", TaskTypes: []string{"ls", "find", "file_hunt", "drives", "usb_enum"}},
			{ID: "T1016", Name: "System Network Configuration Discovery", Tactic: "Discovery", TaskTypes: []string{"net", "netstat", "portscan", "run_egress"}},
			{ID: "T1033", Name: "System Owner/User Discovery", Tactic: "Discovery", TaskTypes: []string{"users", "token_whoami", "ldap_users", "ldap_groups", "ldap_computers", "session_recon"}},
			{ID: "T1120", Name: "Peripheral Device Discovery", Tactic: "Discovery", TaskTypes: []string{"usb_enum", "drives"}},
			{ID: "T1217", Name: "Browser Information Discovery", Tactic: "Discovery", TaskTypes: []string{"browser_history", "wechat_history"}},
			{ID: "T1010", Name: "Application Window Discovery", Tactic: "Discovery", TaskTypes: []string{"window_list"}},
		},
	},
	{
		Tactic: "Collection",
		Techniques: []attackTechnique{
			{ID: "T1113", Name: "Screen Capture", Tactic: "Collection", TaskTypes: []string{"screenshot", "screenshot_window", "screen_stream_start", "screen_trigger_start", "webcam"}},
			{ID: "T1056", Name: "Input Capture", Tactic: "Collection", TaskTypes: []string{"keylogger_start", "clipboard_get"}},
			{ID: "T1005", Name: "Data from Local System", Tactic: "Collection", TaskTypes: []string{"wechat_history", "browser_history", "cookie_export"}},
		},
	},
	{
		Tactic: "Lateral Movement",
		Techniques: []attackTechnique{
			{ID: "T1021", Name: "Remote Services", Tactic: "Lateral Movement", TaskTypes: []string{"lateral", "lateral_wmi", "lateral_psexec", "lateral_winrm", "lateral_dcom", "lateral_scf", "ssh_lateral"}},
			{ID: "T1570", Name: "Lateral Tool Transfer", Tactic: "Lateral Movement", TaskTypes: []string{"upload", "scp_upload", "download_url", "usb_drop"}},
			{ID: "T1091", Name: "Replication Through Removable Media", Tactic: "Lateral Movement", TaskTypes: []string{"usb_drop"}},
		},
	},
	{
		Tactic: "Command and Control",
		Techniques: []attackTechnique{
			{ID: "T1071", Name: "Application Layer Protocol", Tactic: "Command and Control", TaskTypes: []string{"beacon_now", "set_sleep", "profile_rotate", "set_c2_mode", "set_sleep_mode"}},
			{ID: "T1573", Name: "Encrypted Channel", Tactic: "Command and Control", TaskTypes: []string{}},
			{ID: "T1095", Name: "Non-Application Layer Protocol", Tactic: "Command and Control", TaskTypes: []string{"gossip_discover"}},
		},
	},
}

// taskTypesInUse reports the distinct task types observed across the whole
// deployment (agentID == "") or for one agent. A database error is returned
// to the caller: a Navigator layer must fail rather than claim zero coverage
// when the census could not be read.
func (s *Server) taskTypesInUse(agentID string) (map[string]bool, error) {
	q := s.db.Table("tasks").Select("DISTINCT type")
	if agentID != "" {
		q = q.Where("agent_id = ?", agentID)
	}
	var types []string
	if err := q.Pluck("type", &types).Error; err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(types))
	for _, taskType := range types {
		used[taskType] = true
	}
	return used, nil
}

// handleAttackCoverage returns MITRE ATT&CK technique coverage for an agent.
// GET /attack/coverage?agent_id=
func (s *Server) handleAttackCoverage(c *gin.Context) {
	agentID := c.Query("agent_id")

	used, err := s.taskTypesInUse(agentID)
	if err != nil {
		// This page has always degraded to an empty census with a log line;
		// keep that behaviour. The Navigator export fails loudly instead.
		if agentID != "" {
			slog.Error("Failed to pluck task types for agent", "agent_id", agentID, "err", err)
		} else {
			slog.Error("Failed to pluck task types", "err", err)
		}
		used = map[string]bool{}
	}

	usedTaskTypes := make([]string, 0, len(used))
	for k := range used {
		usedTaskTypes = append(usedTaskTypes, k)
	}

	var tactics []map[string]interface{}
	total := 0
	totalCovered := 0
	for _, grp := range attackTacticMap {
		techs := make([]map[string]interface{}, 0, len(grp.Techniques))
		covered := 0
		for _, tech := range grp.Techniques {
			isCovered := false
			for _, tt := range tech.TaskTypes {
				if used[tt] {
					isCovered = true
					break
				}
			}
			if isCovered {
				covered++
				totalCovered++
			}
			total++
			techs = append(techs, map[string]interface{}{
				"id":         tech.ID,
				"name":       tech.Name,
				"tactic":     tech.Tactic,
				"task_types": tech.TaskTypes,
				"covered":    isCovered,
			})
		}
		tactics = append(tactics, map[string]interface{}{
			"tactic":     grp.Tactic,
			"techniques": techs,
			"covered":    covered,
			"total":      len(grp.Techniques),
		})
	}

	respond(c, gin.H{
		"tactics":         tactics,
		"total_covered":   totalCovered,
		"total":           total,
		"used_task_types": usedTaskTypes,
	})
}

// navigatorTacticSlugs maps the display names in attackTacticMap to the short
// tactic names ATT&CK Navigator expects in a layer.
var navigatorTacticSlugs = map[string]string{
	"Execution":            "execution",
	"Persistence":          "persistence",
	"Privilege Escalation": "privilege-escalation",
	"Defense Evasion":      "defense-evasion",
	"Credential Access":    "credential-access",
	"Discovery":            "discovery",
	"Collection":           "collection",
	"Lateral Movement":     "lateral-movement",
	"Command and Control":  "command-and-control",
}

// handleMitreNavigator exports the same coverage catalogue as an ATT&CK
// Navigator layer. GET /api/mitre/navigator?agent_id=
func (s *Server) handleMitreNavigator(c *gin.Context) {
	agentID := c.Query("agent_id")
	used, err := s.taskTypesInUse(agentID)
	if err != nil {
		slog.Error("Failed to load task types for Navigator layer", "agent_id", agentID, "err", err)
		handleQueryError(c, err, "Failed to build ATT&CK Navigator layer")
		return
	}

	type layerTechnique struct {
		TechniqueID string              `json:"techniqueID"`
		Tactic      string              `json:"tactic"`
		Color       string              `json:"color"`
		Enabled     bool                `json:"enabled"`
		Comment     string              `json:"comment"`
		Metadata    []map[string]string `json:"metadata"`
		taskTypes   []string
	}
	techniques := make([]layerTechnique, 0, len(attackTacticMap)*3)
	index := make(map[string]int, len(attackTacticMap)*3)
	for _, grp := range attackTacticMap {
		slug, ok := navigatorTacticSlugs[grp.Tactic]
		if !ok {
			continue
		}
		for _, tech := range grp.Techniques {
			key := tech.ID + "\x00" + slug
			if i, exists := index[key]; exists {
				techniques[i].taskTypes = append(techniques[i].taskTypes, tech.TaskTypes...)
				continue
			}
			index[key] = len(techniques)
			techniques = append(techniques, layerTechnique{
				TechniqueID: tech.ID,
				Tactic:      slug,
				Enabled:     true,
				taskTypes:   append([]string(nil), tech.TaskTypes...),
			})
		}
	}

	for i := range techniques {
		sort.Strings(techniques[i].taskTypes)
		value := strings.Join(techniques[i].taskTypes, ", ")
		covered := false
		for _, taskType := range techniques[i].taskTypes {
			if used[taskType] {
				covered = true
				break
			}
		}
		if covered {
			techniques[i].Color = "#2f8f46"
			techniques[i].Comment = "Covered by: " + value
		} else {
			techniques[i].Color = "#c7c7c7"
			techniques[i].Comment = "No dispatchable task type mapped"
		}
		techniques[i].Metadata = []map[string]string{{"name": "task_types", "value": value}}
	}

	layer := map[string]interface{}{
		"name":                          "Sovereign ATT&CK coverage",
		"versions":                      map[string]string{"attack": "14", "navigator": "4.5"},
		"domain":                        "enterprise-attack",
		"description":                   "Generated from the Sovereign dispatchable task catalogue and observed task types",
		"showTacticRowBackground":       true,
		"tacticRowBackground":           "#dddddd",
		"selectTechniquesAcrossTactics": true,
		"techniques":                    techniques,
	}
	respond(c, gin.H{"layer": layer})
}

// handleMitrePhases returns kill-chain phase coverage across all campaigns.
// GET /mitre/phases
func (s *Server) handleMitrePhases(c *gin.Context) {
	var campaigns []db.Campaign
	if err := s.db.Preload("Agents").Limit(500).Find(&campaigns).Error; err != nil {
		slog.Error("Failed to query MITRE campaigns", "err", err)
	}

	phaseTasks := map[string]int{}
	phaseCampaigns := map[string]map[string]bool{}

	// Collect all agent IDs across campaigns
	allAgentIDs := make([]string, 0)
	for _, camp := range campaigns {
		for _, a := range camp.Agents {
			allAgentIDs = append(allAgentIDs, a.ID)
		}
	}

	// Batch-load all tasks for these agents
	type agentTask struct {
		AgentID string
		Type    string
	}
	var allTasks []agentTask
	if len(allAgentIDs) > 0 {
		if err := s.db.Table("tasks").Select("agent_id, type").Where("agent_id IN ?", allAgentIDs).Find(&allTasks).Error; err != nil {
			slog.Error("Failed to query MITRE tasks", "err", err)
		}
	}

	// Index tasks by agent
	tasksByAgent := map[string][]string{}
	for _, t := range allTasks {
		tasksByAgent[t.AgentID] = append(tasksByAgent[t.AgentID], t.Type)
	}

	for _, camp := range campaigns {
		campPhaseSet := map[string]bool{}
		for _, a := range camp.Agents {
			for _, tt := range tasksByAgent[a.ID] {
				p := taskPhase(tt)
				phaseTasks[p]++
				campPhaseSet[p] = true
			}
		}
		for p := range campPhaseSet {
			if phaseCampaigns[p] == nil {
				phaseCampaigns[p] = map[string]bool{}
			}
			phaseCampaigns[p][camp.ID] = true
		}
	}

	phases := make([]map[string]interface{}, 0, len(phaseOrder))
	for _, p := range phaseOrder {
		covered := 0
		if set, ok := phaseCampaigns[p]; ok {
			covered = len(set)
		}
		phases = append(phases, map[string]interface{}{
			"phase":             p,
			"total_tasks":       phaseTasks[p],
			"campaigns_covered": covered,
			"total_campaigns":   len(campaigns),
		})
	}

	respond(c, phases)
}

// mitreTaskTacticIndex maps every task type in attackTacticMap to its tactic
// (first match wins) so task rows can be attributed to a technique column.
func mitreTaskTacticIndex() map[string]string {
	idx := map[string]string{}
	for _, grp := range attackTacticMap {
		for _, tech := range grp.Techniques {
			for _, tt := range tech.TaskTypes {
				if _, ok := idx[tt]; !ok {
					idx[tt] = tech.Tactic
				}
			}
		}
	}
	return idx
}

// handleMitreHeatmap returns daily task counts per MITRE tactic over a time
// range, for the usage heatmap on the attack page.
// GET /api/mitre/heatmap?range=7d|30d|90d
func (s *Server) handleMitreHeatmap(c *gin.Context) {
	rangeParam := c.DefaultQuery("range", "30d")
	var days int
	switch rangeParam {
	case "7d":
		days = 7
	case "90d":
		days = 90
	default:
		days = 30
		rangeParam = "30d"
	}
	if days > 120 {
		days = 120 // hard cap: the heatmap is unreadable beyond that anyway
	}

	start := time.Now().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)

	type row struct {
		Tactic string
		Bucket string
		Count  int64
	}
	tacticIdx := mitreTaskTacticIndex()

	var types []string
	if err := s.db.Table("tasks").
		Where("created_at >= ?", start).
		Distinct().Pluck("type", &types).Error; err != nil {
		slog.Error("Heatmap: failed to pluck task types", "err", err)
		types = nil
	}

	buckets := make([]row, 0, days*len(types))
	// SQLite: group by day string. We bucket in Go so the tactic attribution
	// (which needs the full type list) and date formatting stay portable.
	type dayType struct {
		Type  string
		Day   string
		Count int64
	}
	var counts []dayType
	if len(types) > 0 {
		if err := s.db.Table("tasks").
			Select("type, "+dayStringSelectExpr(s.db, "created_at")+", COUNT(*) as count").
			Where("created_at >= ? AND type IN ?", start, types).
			Group("type, day").Scan(&counts).Error; err != nil {
			slog.Error("Heatmap: failed to group tasks", "err", err)
			counts = nil
		}
	}
	for _, ct := range counts {
		tactic := tacticIdx[ct.Type]
		if tactic == "" {
			continue
		}
		buckets = append(buckets, row{Tactic: tactic, Bucket: ct.Day, Count: ct.Count})
	}

	// Merge per (bucket, tactic).
	merged := map[string]int64{}
	for _, b := range buckets {
		key := b.Bucket + "|" + b.Tactic
		merged[key] += b.Count
	}

	// Build dense day axis.
	dayKeys := make([]string, 0, days)
	base := time.Now().Truncate(24 * time.Hour)
	for i := days - 1; i >= 0; i-- {
		dayKeys = append(dayKeys, base.AddDate(0, 0, -i).Format("2006-01-02"))
	}

	// Stable tactic order follows attackTacticMap.
	tacticOrder := make([]string, 0, len(attackTacticMap))
	seen := map[string]bool{}
	for _, grp := range attackTacticMap {
		if !seen[grp.Tactic] {
			tacticOrder = append(tacticOrder, grp.Tactic)
			seen[grp.Tactic] = true
		}
	}

	cells := make([]map[string]interface{}, 0, len(dayKeys)*len(tacticOrder))
	for _, day := range dayKeys {
		for _, tactic := range tacticOrder {
			cells = append(cells, map[string]interface{}{
				"date":   day,
				"tactic": tactic,
				"count":  merged[day+"|"+tactic],
			})
		}
	}

	respond(c, gin.H{
		"days":        dayKeys,
		"tactics":     tacticOrder,
		"cells":       cells,
		"range":       rangeParam,
		"total_tasks": sumCounts(merged),
	})
}

func sumCounts(m map[string]int64) int64 {
	var total int64
	for _, v := range m {
		total += v
	}
	return total
}
