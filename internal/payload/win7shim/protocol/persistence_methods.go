package protocol

// PersistenceMethod values are the method field of persistence_add /
// persistence_remove. The wire format is still method|args for compatibility;
// this registry exists so the console, gates and docs share one list of names
// instead of rediscovering drift between install, detection and removal.
type PersistenceMethod string

const (
	PersistenceRegistry       PersistenceMethod = "registry"
	PersistenceScheduledTask  PersistenceMethod = "scheduled_task"
	PersistenceStartupFolder  PersistenceMethod = "startup_folder"
	PersistenceWMI            PersistenceMethod = "wmi"
	PersistenceService        PersistenceMethod = "service"
	PersistenceImageFile      PersistenceMethod = "image_file"
	PersistenceCOMHijack      PersistenceMethod = "com_hijack"
	PersistenceDLLSearchOrder PersistenceMethod = "dll_search_order"
	PersistenceTypeLib        PersistenceMethod = "typelib"
	PersistenceLogonScript    PersistenceMethod = "logon_script"
	PersistenceCron           PersistenceMethod = "cron"
	PersistenceCrontab        PersistenceMethod = "crontab"
	PersistenceAutostart      PersistenceMethod = "autostart"
	PersistenceXDG            PersistenceMethod = "xdg"
	PersistenceSystemd        PersistenceMethod = "systemd"
	PersistenceSSH            PersistenceMethod = "ssh"
	PersistenceSSHAuthorized  PersistenceMethod = "ssh_authorized_keys"
	PersistenceLaunchAgent    PersistenceMethod = "launchagent"
	PersistenceLaunchd        PersistenceMethod = "launchd"
	PersistencePlist          PersistenceMethod = "plist"
	PersistenceLoginItem      PersistenceMethod = "loginitem"
	PersistenceLoginItems     PersistenceMethod = "loginitems"
)

// PersistenceMethods is the complete, ordered value set. check:persistence-methods
// compares it against every agent-side switch, in both directions.
var PersistenceMethods = []PersistenceMethod{
	PersistenceRegistry, PersistenceScheduledTask, PersistenceStartupFolder,
	PersistenceWMI, PersistenceService, PersistenceImageFile,
	PersistenceCOMHijack, PersistenceDLLSearchOrder, PersistenceTypeLib,
	PersistenceLogonScript, PersistenceCron, PersistenceCrontab,
	PersistenceAutostart, PersistenceXDG, PersistenceSystemd,
	PersistenceSSH, PersistenceSSHAuthorized, PersistenceLaunchAgent,
	PersistenceLaunchd, PersistencePlist, PersistenceLoginItem, PersistenceLoginItems,
}

// IsPersistenceMethod reports whether s is a method the agent family knows.
func IsPersistenceMethod(s string) bool {
	for _, m := range PersistenceMethods {
		if string(m) == s {
			return true
		}
	}
	return false
}
