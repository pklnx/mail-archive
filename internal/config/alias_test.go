package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/crypto"
)

// oldNames16 are the variables the binary of 1.6.0 read (#585): every one
// keeps working under this name. A name dropped from the alias table by
// accident fails TestAliasTable.
var oldNames16 = []string{
	"MAIL_ARCHIVE_ALERT_AFTER_FAILURES", "MAIL_ARCHIVE_ALLOWED_HOSTS", "MAIL_ARCHIVE_ARCHIVE_KEY",
	"MAIL_ARCHIVE_ARCHIVE_KEY_FILE", "MAIL_ARCHIVE_ATTEST_INTERVAL", "MAIL_ARCHIVE_ATTEST_KEY_FILE",
	"MAIL_ARCHIVE_COMMAND", "MAIL_ARCHIVE_DATABASE_URL", "MAIL_ARCHIVE_DATA_DIR",
	"MAIL_ARCHIVE_ENCRYPTION_AT_SETUP", "MAIL_ARCHIVE_FILL_MAX_FOLDER_MESSAGES", "MAIL_ARCHIVE_IMAP_IDLE_TIMEOUT",
	"MAIL_ARCHIVE_IMAP_MAX_FOLDERS", "MAIL_ARCHIVE_JOB_WORKERS", "MAIL_ARCHIVE_KEYRING_COPY",
	"MAIL_ARCHIVE_LOG_LEVEL", "MAIL_ARCHIVE_MAIL_FROM", "MAIL_ARCHIVE_MAIL_PASSWORD_FILE",
	"MAIL_ARCHIVE_MAIL_URL", "MAIL_ARCHIVE_MIGRATE_ON_START", "MAIL_ARCHIVE_MIN_FREE_DISK",
	"MAIL_ARCHIVE_NOTIFY_WEBHOOK_AUTHORIZATION", "MAIL_ARCHIVE_NOTIFY_WEBHOOK_DETAIL",
	"MAIL_ARCHIVE_NOTIFY_WEBHOOK_FORMAT", "MAIL_ARCHIVE_NOTIFY_WEBHOOK_URL", "MAIL_ARCHIVE_OPERATOR",
	"MAIL_ARCHIVE_PUBLIC_URL", "MAIL_ARCHIVE_RECONCILE_INTERVAL", "MAIL_ARCHIVE_RECONCILE_MAX_MESSAGES",
	"MAIL_ARCHIVE_REQUIRE_2FA", "MAIL_ARCHIVE_RETENTION_FILE_GRACE", "MAIL_ARCHIVE_SECRET_KEY",
	"MAIL_ARCHIVE_SECRET_KEY_FILE", "MAIL_ARCHIVE_SETUP_TOKEN", "MAIL_ARCHIVE_SETUP_TOKEN_FILE",
	"MAIL_ARCHIVE_SMTP_ALLOWED_NETWORKS", "MAIL_ARCHIVE_SMTP_AUTH", "MAIL_ARCHIVE_SMTP_HOSTNAME",
	"MAIL_ARCHIVE_SMTP_LISTEN", "MAIL_ARCHIVE_SMTP_MAX_CONNS_PER_ADDR", "MAIL_ARCHIVE_SMTP_MAX_SIZE",
	"MAIL_ARCHIVE_SMTP_TLS_CERT", "MAIL_ARCHIVE_SMTP_TLS_KEY", "MAIL_ARCHIVE_STORAGE_QUOTA",
	"MAIL_ARCHIVE_STORAGE_SMTP_OVERFLOW_PERCENT", "MAIL_ARCHIVE_STORAGE_WARN_PERCENT",
	"MAIL_ARCHIVE_SYNC_INTERVAL", "MAIL_ARCHIVE_SYNC_INTERVAL_MIN", "MAIL_ARCHIVE_TRUSTED_PROXIES",
}

// clearEnv unsets every variable of the table under both names for the
// test (empty counts as unset).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range AliasedNames() {
		t.Setenv(name, "")
		t.Setenv(OldName(name), "")
	}
}

// TestAliasTable (#585): the table holds exactly the 49 names of 1.6.0,
// each under KEEPIA_ARCHIVE_ with the same suffix.
func TestAliasTable(t *testing.T) {
	names := AliasedNames()
	if len(names) != 49 || len(oldNames16) != 49 {
		t.Fatalf("%d names in the table, %d in 1.6.0; want 49", len(names), len(oldNames16))
	}
	var olds []string
	for _, name := range names {
		if !strings.HasPrefix(name, EnvPrefix) {
			t.Errorf("%s does not start with %s", name, EnvPrefix)
		}
		old := OldName(name)
		if old != OldEnvPrefix+strings.TrimPrefix(name, EnvPrefix) {
			t.Errorf("old name of %s: %q", name, old)
		}
		olds = append(olds, old)
	}
	slices.Sort(olds)
	if !slices.Equal(olds, oldNames16) {
		t.Errorf("old names:\n%q\nwant the names of 1.6.0:\n%q", olds, oldNames16)
	}
	if OldName("KEEPIA_ARCHIVE_UNKNOWN") != "" || OldName("PATH") != "" {
		t.Error("an old name for a variable outside the table")
	}
}

// TestAliasResolution (#585): for every name of the table the five cases.
// The new name wins; an old name alone works and is reported; both equal
// are reported; both different are refused, naming both variables and no
// value; neither gives "" (the caller's default). Empty counts as unset.
func TestAliasResolution(t *testing.T) {
	for _, name := range AliasedNames() {
		old := OldName(name)
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				desc            string
				newVal, oldVal  string
				want            string
				reported, error bool
			}{
				{"new only", "v-new", "", "v-new", false, false},
				{"old only", "", "v-old", "v-old", true, false},
				{"both equal", "v-same", "v-same", "v-same", true, false},
				{"both different", "v-new", "v-old", "v-new", false, true},
				{"neither", "", "", "", false, false},
			} {
				clearEnv(t)
				t.Setenv(name, tc.newVal)
				t.Setenv(old, tc.oldVal)
				if got := Getenv(name); got != tc.want {
					t.Errorf("%s: Getenv = %q, want %q", tc.desc, got, tc.want)
				}
				if got := EnvValue(os.Environ(), name); got != tc.want {
					t.Errorf("%s: EnvValue = %q, want %q", tc.desc, got, tc.want)
				}
				inUse, err := ScanEnv(os.Environ())
				reported := slices.Contains(inUse, EnvAlias{Name: name, Old: old})
				if reported != tc.reported || len(inUse) > 1 {
					t.Errorf("%s: reported %v", tc.desc, inUse)
				}
				var conflict *EnvConflictError
				if errors.As(err, &conflict) != tc.error {
					t.Fatalf("%s: error %v", tc.desc, err)
				}
				if tc.error {
					msg := err.Error()
					want := name + " and its old name " + old + " are both set, to different values: remove " + old
					if msg != want {
						t.Errorf("%s: message %q, want %q", tc.desc, msg, want)
					}
					if strings.Contains(msg, "v-") {
						t.Errorf("%s: the message shows a value: %q", tc.desc, msg)
					}
				}
			}
		})
	}
}

// TestScanEnv (#585): the old names in use come sorted, several conflicts
// are named together, a name held twice counts with its first value (as
// for os.Getenv), and other variables are ignored.
func TestScanEnv(t *testing.T) {
	inUse, err := ScanEnv([]string{
		"MAIL_ARCHIVE_SYNC_INTERVAL=2h", "MAIL_ARCHIVE_DATA_DIR=/srv", "KEEPIA_ARCHIVE_DATA_DIR=/srv",
		"MAIL_ARCHIVE_BENCH=1", "MAIL_ARCHIVE_UNKNOWN=x", "PATH=/bin", "NOEQUALS",
		"MAIL_ARCHIVE_LOG_LEVEL=", "KEEPIA_ARCHIVE_LOG_LEVEL=debug",
	})
	want := []EnvAlias{{EnvDataDir, "MAIL_ARCHIVE_DATA_DIR"}, {EnvSyncInterval, "MAIL_ARCHIVE_SYNC_INTERVAL"}}
	if err != nil || !slices.Equal(inUse, want) {
		t.Errorf("got %v %v, want %v", inUse, err, want)
	}
	// The first of two values counts: equal to the new name, no conflict.
	if _, err := ScanEnv([]string{"MAIL_ARCHIVE_DATA_DIR=/a", "MAIL_ARCHIVE_DATA_DIR=/b", "KEEPIA_ARCHIVE_DATA_DIR=/a"}); err != nil {
		t.Errorf("first value: %v", err)
	}
	_, err = ScanEnv([]string{"MAIL_ARCHIVE_SECRET_KEY=a", "KEEPIA_ARCHIVE_SECRET_KEY=b",
		"MAIL_ARCHIVE_DATA_DIR=/a", "KEEPIA_ARCHIVE_DATA_DIR=/b", "MAIL_ARCHIVE_SYNC_INTERVAL=2h"})
	if err == nil || err.Error() != "KEEPIA_ARCHIVE_DATA_DIR and its old name MAIL_ARCHIVE_DATA_DIR are both set, to different values: remove MAIL_ARCHIVE_DATA_DIR; "+
		"KEEPIA_ARCHIVE_SECRET_KEY and its old name MAIL_ARCHIVE_SECRET_KEY are both set, to different values: remove MAIL_ARCHIVE_SECRET_KEY" {
		t.Errorf("two conflicts: %v", err)
	}
	if got := DeprecationWarning(want); got != "old environment variable names in use: MAIL_ARCHIVE_DATA_DIR (now KEEPIA_ARCHIVE_DATA_DIR), "+
		"MAIL_ARCHIVE_SYNC_INTERVAL (now KEEPIA_ARCHIVE_SYNC_INTERVAL); rename them to the new names. "+
		"The old names keep working in 1.x and will be removed in a later major release" {
		t.Errorf("warning: %q", got)
	}
}

// TestLoadOldNames (#585): a configuration with only the old names (as an
// unchanged .env of 1.6) loads as with the new ones, also the variables
// read through ParseIMAPLimits and ParseStorage; both names set to
// different values are refused by Load.
func TestLoadOldNames(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"MAIL_ARCHIVE_DATABASE_URL": "postgres://x/y", "MAIL_ARCHIVE_DATA_DIR": dir,
		"MAIL_ARCHIVE_SYNC_INTERVAL": "2h", "MAIL_ARCHIVE_IMAP_MAX_FOLDERS": "500",
		"MAIL_ARCHIVE_STORAGE_QUOTA": "10G", "MAIL_ARCHIVE_SECRET_KEY": key,
		"MAIL_ARCHIVE_LOG_LEVEL": "debug", "MAIL_ARCHIVE_ENCRYPTION_AT_SETUP": "off",
	} {
		t.Setenv(name, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://x/y" || cfg.DataDir != dir || cfg.SyncInterval != 2*time.Hour ||
		cfg.IMAP.MaxFolders != 500 || cfg.Storage.Quota != 10<<30 || cfg.SecretKeySource != EnvSecretKey ||
		cfg.LogLevel != "debug" || DataDirFromEnv() != dir {
		t.Errorf("old names: %+v", cfg)
	}
	// The new name next to an equal old one: the same.
	t.Setenv(EnvSyncInterval, "2h")
	if cfg, err = Load(); err != nil || cfg.SyncInterval != 2*time.Hour {
		t.Errorf("both equal: %v %v", cfg, err)
	}
	// Different: refused, whichever variable Load reads.
	t.Setenv(EnvSyncInterval, "3h")
	var conflict *EnvConflictError
	if _, err := Load(); !errors.As(err, &conflict) || len(conflict.Conflicts) != 1 || conflict.Conflicts[0].Name != EnvSyncInterval {
		t.Errorf("both different: %v", err)
	}
}

// TestFilePairsAcrossNames (#585): each name of a "value or file" pair is
// resolved on its own, then the "not both" rule applies as before: the
// old value with the new file (and the reverse) is refused like both old
// names together.
func TestFilePairsAcrossNames(t *testing.T) {
	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "key")
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(key), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ value, file string }{
		{"MAIL_ARCHIVE_SECRET_KEY", EnvSecretKeyFile},
		{EnvSecretKey, "MAIL_ARCHIVE_SECRET_KEY_FILE"},
		{"MAIL_ARCHIVE_SECRET_KEY", "MAIL_ARCHIVE_SECRET_KEY_FILE"},
	} {
		clearEnv(t)
		t.Setenv(EnvDataDir, t.TempDir())
		t.Setenv(tc.value, key)
		t.Setenv(tc.file, keyFile)
		var keyErr *SecretKeyError
		if _, _, err := SecretKeyFromEnv(); !errors.As(err, &keyErr) || !strings.Contains(err.Error(), "both "+EnvSecretKeyFile+" and "+EnvSecretKey) {
			t.Errorf("%s with %s: %v", tc.value, tc.file, err)
		}
	}
	// The old file name alone works.
	clearEnv(t)
	t.Setenv(EnvDataDir, t.TempDir())
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY_FILE", keyFile)
	if got, source, err := SecretKeyFromEnv(); err != nil || len(got) != 32 || source != "file "+keyFile {
		t.Errorf("old file name: %v %q", err, source)
	}

	for _, tc := range []struct{ value, file string }{
		{"MAIL_ARCHIVE_ARCHIVE_KEY", EnvArchiveKeyFile},
		{EnvArchiveKey, "MAIL_ARCHIVE_ARCHIVE_KEY_FILE"},
	} {
		clearEnv(t)
		t.Setenv(EnvDataDir, t.TempDir())
		t.Setenv(tc.value, key)
		t.Setenv(tc.file, keyFile)
		if _, _, err := ArchiveKeyFromEnv(); err == nil || !strings.Contains(err.Error(), EnvArchiveKeyFile) || !strings.Contains(err.Error(), EnvArchiveKey) {
			t.Errorf("%s with %s: %v", tc.value, tc.file, err)
		}
	}
}

// TestDefaultDataDir (#585): without the variable the data directory is
// ./data, or what main set from the build (/data in the image).
func TestDefaultDataDir(t *testing.T) {
	clearEnv(t)
	t.Cleanup(func() { defaultDataDir = "./data" })
	if got := DataDirFromEnv(); got != "./data" {
		t.Errorf("default %q", got)
	}
	SetDefaultDataDir("")
	if got := DataDirFromEnv(); got != "./data" {
		t.Errorf("empty build value: %q", got)
	}
	SetDefaultDataDir("/data")
	if got := DataDirFromEnv(); got != "/data" {
		t.Errorf("build value: %q", got)
	}
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", "/srv/old")
	if got := DataDirFromEnv(); got != "/srv/old" {
		t.Errorf("old name: %q", got)
	}
	t.Setenv(EnvDataDir, "/srv/new")
	t.Setenv("MAIL_ARCHIVE_DATA_DIR", "")
	if got := DataDirFromEnv(); got != "/srv/new" {
		t.Errorf("new name: %q", got)
	}
}

// directRead matches a read of the environment that would bypass the
// alias table: os.Getenv or os.LookupEnv of a name of the table, as a
// constant of this package (directReadHere inside it) or a literal.
var (
	directRead     = regexp.MustCompile(`os\.(Getenv|LookupEnv)\((config\.Env[A-Z]\w*|envKeyringCopy|"(KEEPIA|MAIL)_ARCHIVE_)`)
	directReadHere = regexp.MustCompile(`os\.(Getenv|LookupEnv)\(Env[A-Z]\w*`)
)

// TestNoDirectEnvRead (#585): no code of the module reads a variable of
// the table without Getenv, so the old names work everywhere. Tests may
// (they set what they read).
func TestNoDirectEnvRead(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path) //nolint:gosec // a file of this repository
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				if directRead.MatchString(line) || dir == "internal" && filepath.Base(filepath.Dir(path)) == "config" && directReadHere.MatchString(line) {
					found = append(found, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) > 0 {
		t.Errorf("read without config.Getenv:\n%s", strings.Join(found, "\n"))
	}
}
