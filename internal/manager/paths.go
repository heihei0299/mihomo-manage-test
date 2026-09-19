package manager

const (
	filePermUserRW  = 0644
	filePermUserRWX = 0755

	installRoot = "/opt/mihomo"
	managerRoot = "/opt/mihomo-manager"

	binaryPath                 = installRoot + "/bin/mihomo"
	configDir                  = installRoot + "/etc"
	OverrideFilePath           = installRoot + "/etc/override.yaml"
	RoutingRulesPath           = installRoot + "/etc/rules.txt"
	configYAML                 = installRoot + "/etc/config.yaml"
	stateDir                   = managerRoot + "/state"
	subscriptionDataFile       = managerRoot + "/state/subscription-data.txt"
	subscriptionURLFile        = managerRoot + "/state/subscription-url.txt"
	subscriptionSourceFile     = managerRoot + "/state/subscription-source.txt"
	subscriptionUpdateLockFile = managerRoot + "/state/config-update.lock"
	configApplyStatusFile      = managerRoot + "/state/config-apply-status.json"
	configApplyTransactionFile = managerRoot + "/state/config-apply-transaction.json"
)
