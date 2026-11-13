package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Storage quota settings (#397, part 2). The operator sets them in the
// environment; group quotas live in the database (`group set-quota`).
const (
	// EnvStorageQuota is the instance's quota in message bytes after
	// deduplication, like 500GiB; empty or 0 means no quota.
	EnvStorageQuota = "KEEPIA_ARCHIVE_STORAGE_QUOTA"
	// EnvStorageWarnPercent is the share of a quota at which its scope is
	// in warn.
	EnvStorageWarnPercent = "KEEPIA_ARCHIVE_STORAGE_WARN_PERCENT"
	// EnvStorageSMTPOverflowPercent is how far above 100 % of a quota the
	// SMTP server still accepts journal copies.
	EnvStorageSMTPOverflowPercent = "KEEPIA_ARCHIVE_STORAGE_SMTP_OVERFLOW_PERCENT"
	// EnvMinFreeDisk is the free space of the data directory's file system
	// below which nothing new is stored (disk_low); 0 turns it off.
	EnvMinFreeDisk = "KEEPIA_ARCHIVE_MIN_FREE_DISK"
)

// Defaults and ranges of the storage settings.
const (
	DefaultStorageWarnPercent         = 80
	DefaultStorageSMTPOverflowPercent = 10
	DefaultMinFreeDisk                = 1 << 30
	// MaxStorageSMTPOverflowPercent: at most the quota again.
	MaxStorageSMTPOverflowPercent = 100
)

// Storage holds the storage limits of the instance.
type Storage struct {
	// Quota is the instance's quota in message bytes; 0 means none.
	Quota int64
	// WarnPercent (1-99) and OverflowPercent (0-100) apply to the instance
	// quota and to every group quota.
	WarnPercent, OverflowPercent int
	// MinFreeDisk is the disk reserve in bytes; 0 turns it off.
	MinFreeDisk int64
}

// ParseStorage reads the storage settings through getenv: an unset
// variable gives its default, a bad value is an error naming the variable.
func ParseStorage(getenv func(string) string) (Storage, error) {
	s := Storage{WarnPercent: DefaultStorageWarnPercent, OverflowPercent: DefaultStorageSMTPOverflowPercent, MinFreeDisk: DefaultMinFreeDisk}
	if v := strings.TrimSpace(getenv(EnvStorageQuota)); v != "" {
		n, err := ParseByteSize(v)
		if err != nil {
			return s, fmt.Errorf("%s: want a size like 500GiB or 2TiB, or 0 for no quota, got %q", EnvStorageQuota, v)
		}
		s.Quota = n
	}
	if v := strings.TrimSpace(getenv(EnvStorageWarnPercent)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 99 {
			return s, fmt.Errorf("%s: want a number from 1 to 99, got %q", EnvStorageWarnPercent, v)
		}
		s.WarnPercent = n
	}
	if v := strings.TrimSpace(getenv(EnvStorageSMTPOverflowPercent)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > MaxStorageSMTPOverflowPercent {
			return s, fmt.Errorf("%s: want a number from 0 to %d, got %q", EnvStorageSMTPOverflowPercent, MaxStorageSMTPOverflowPercent, v)
		}
		s.OverflowPercent = n
	}
	if v := strings.TrimSpace(getenv(EnvMinFreeDisk)); v != "" {
		n, err := ParseByteSize(v)
		if err != nil {
			return s, fmt.Errorf("%s: want a size like 1GiB, or 0 to turn the reserve off, got %q", EnvMinFreeDisk, v)
		}
		s.MinFreeDisk = n
	}
	return s, nil
}

// MaxByteSize bounds every quota and size an operator or admin sets, here
// and through the web API (PATCH /api/groups/{id}): 8 EiB would overflow
// int64 arithmetic in the quota thresholds, and no archive comes near 1 EiB.
const MaxByteSize = 1 << 60

// ParseByteSize reads a byte count: a whole number with an optional unit,
// KiB, MiB, GiB, TiB or PiB (powers of 1024), KB, MB, GB, TB or PB (powers
// of 1000), K, M, G, T or P (powers of 1024) or B, with or without a space
// before it, in any case. "0" is 0. At most 1 EiB.
func ParseByteSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	factor := int64(1)
	for _, u := range []struct {
		suffix string
		factor int64
	}{
		{"PIB", 1 << 50}, {"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"PB", 1e15}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"P", 1 << 50}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	} {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			s, factor = strings.TrimSpace(n), u.factor
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/factor || n*factor > MaxByteSize {
		return 0, fmt.Errorf("invalid size %q", v)
	}
	return n * factor, nil
}
