package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Since 1.7.0 every variable the binary reads is named KEEPIA_ARCHIVE_*
// (#585). The name it had until 1.6, with the same suffix under
// OldEnvPrefix, still works as an alias: read through Getenv, the new name
// wins, an empty value counts as unset, and ScanEnv refuses both names set
// to different values and reports the old names in use for the
// deprecation warning. The old names are removed in a later major release.
const (
	// EnvPrefix starts the name of every variable the binary reads.
	EnvPrefix = "KEEPIA_ARCHIVE_"
	// OldEnvPrefix starts the old names (until 1.6), still read as aliases.
	OldEnvPrefix = "MAIL_ARCHIVE_"
)

// The variables the ./ma wrapper sets for a command.
const (
	// EnvCommand is how the user invokes the CLI, for copy-paste hints:
	// ./ma sets it to "./ma".
	EnvCommand = "KEEPIA_ARCHIVE_COMMAND"
	// EnvKeyringCopy is the keyring copy next to a dump (base64), which
	// ./ma passes to seal-stream, unseal-stream and restore-dump --hold.
	// It carries key material.
	EnvKeyringCopy = "KEEPIA_ARCHIVE_KEYRING_COPY"
)

// aliased lists every variable the binary reads that had an old name: all
// of them. Each is read through Getenv and checked by ScanEnv.
var aliased = []string{
	EnvDatabaseURL, EnvDataDir, EnvSecretKey, EnvSecretKeyFile, EnvLogLevel,
	EnvAllowedHosts, EnvSyncInterval, EnvSyncIntervalMin, EnvRequire2FA,
	EnvReconcileInterval, EnvPublicURL, EnvAlertAfterFailures,
	EnvNotifyWebhookURL, EnvNotifyWebhookFormat, EnvNotifyWebhookAuthorization,
	EnvNotifyWebhookDetail,
	EnvSMTPListen, EnvSMTPAllowedNetworks, EnvSMTPMaxSize, EnvSMTPTLSCert,
	EnvSMTPTLSKey, EnvSMTPHostname, EnvSMTPAuth, EnvSMTPMaxConnsPerAddr,
	EnvTrustedProxies, EnvOperator, EnvAttestKeyFile, EnvAttestInterval,
	EnvRetentionFileGrace, EnvJobWorkers, EnvArchiveKeyFile, EnvArchiveKey,
	EnvFillMaxFolderMessages, EnvReconcileMaxMessages, EnvIMAPMaxFolders,
	EnvIMAPIdleTimeout, EnvMigrateOnStart,
	EnvMailURL, EnvMailPasswordFile, EnvMailFrom,
	EnvSetupTokenFile, EnvSetupToken, EnvEncryptionAtSetup,
	EnvStorageQuota, EnvStorageWarnPercent, EnvStorageSMTPOverflowPercent, EnvMinFreeDisk,
	EnvCommand, EnvKeyringCopy,
}

// oldNames maps each name of aliased to its old name.
var oldNames = func() map[string]string {
	m := make(map[string]string, len(aliased))
	for _, name := range aliased {
		m[name] = OldEnvPrefix + strings.TrimPrefix(name, EnvPrefix)
	}
	return m
}()

// AliasedNames returns the variables that have an old name, sorted.
func AliasedNames() []string {
	names := slices.Clone(aliased)
	slices.Sort(names)
	return names
}

// OldName returns the old name of the variable name, or "" when it has
// none.
func OldName(name string) string { return oldNames[name] }

// Getenv returns the value of the variable name; when that is unset or
// empty, the value of its old name. A conflict (both set to different
// values) is refused by ScanEnv before any command runs, and by Load; here
// the new name wins.
func Getenv(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if old := oldNames[name]; old != "" {
		return os.Getenv(old)
	}
	return ""
}

// EnvAlias is an old variable name that is set, with its new name.
type EnvAlias struct {
	Name, Old string
}

// EnvConflictError is a variable set under both names to different
// values. It names the variables, never their values.
type EnvConflictError struct {
	Conflicts []EnvAlias
}

func (e *EnvConflictError) Error() string {
	parts := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		parts[i] = fmt.Sprintf("%s and its old name %s are both set, to different values: remove %s", c.Name, c.Old, c.Old)
	}
	return strings.Join(parts, "; ")
}

// ScanEnv checks environ (NAME=value, as os.Environ returns it; of a name
// held twice the first counts, as for os.Getenv) against the aliases. It
// returns the old names that are set (not empty), sorted, and an
// *EnvConflictError when a variable is set under both names to different
// values.
func ScanEnv(environ []string) (inUse []EnvAlias, err error) {
	env := envMap(environ)
	var conflicts []EnvAlias
	for _, name := range AliasedNames() {
		old := oldNames[name]
		oldValue := env[old]
		if oldValue == "" {
			continue
		}
		pair := EnvAlias{Name: name, Old: old}
		if v := env[name]; v != "" && v != oldValue {
			conflicts = append(conflicts, pair)
			continue
		}
		inUse = append(inUse, pair)
	}
	slices.SortFunc(inUse, func(a, b EnvAlias) int { return strings.Compare(a.Old, b.Old) })
	if conflicts != nil {
		return inUse, &EnvConflictError{Conflicts: conflicts}
	}
	return inUse, nil
}

// EnvValue is Getenv on environ instead of the process environment.
func EnvValue(environ []string, name string) string {
	env := envMap(environ)
	if v := env[name]; v != "" {
		return v
	}
	if old := oldNames[name]; old != "" {
		return env[old]
	}
	return ""
}

// envMap maps the names of environ to their values; of a name held twice
// the first counts, as for os.Getenv.
func envMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}
	return env
}

// DeprecationWarning is the text of the warning about old names in use:
// each old name with its new one. It names no value.
func DeprecationWarning(inUse []EnvAlias) string {
	parts := make([]string, len(inUse))
	for i, a := range inUse {
		parts[i] = a.Old + " (now " + a.Name + ")"
	}
	return "old environment variable names in use: " + strings.Join(parts, ", ") +
		"; rename them to the new names. The old names keep working in 1.x and will be removed in a later major release"
}
