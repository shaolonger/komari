// Package runtimeprofile resolves bounded runtime settings from an explicit
// operator profile and the process cgroup. Profiles tune performance only; they
// never change authentication, authorization, durability or network policy.
package runtimeprofile

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

const (
	ProfileAuto     = "auto"
	ProfileNano     = "nano"
	ProfileStandard = "standard"
	ProfileScale    = "scale"
)

const (
	gibibyte = int64(1024 * 1024 * 1024)
	mebibyte = int64(1024 * 1024)
)

type Limits struct {
	CPUs        int
	MemoryBytes int64
}

type Profile struct {
	Name                  string
	DetectedCPUs          int
	DetectedMemoryBytes   int64
	GoMemoryLimitBytes    int64
	SQLiteReadConnections int
	SQLiteCacheKiB        int
	SQLiteMmapBytes       int64
	SQLiteTempStore       string
	PingWorkers           int
	PingQueueCapacity     int
}

var (
	currentOnce sync.Once
	current     Profile
	currentErr  error
	applyOnce   sync.Once
)

func Resolve(requested string, limits Limits) (Profile, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		requested = ProfileAuto
	}
	if limits.CPUs <= 0 {
		limits.CPUs = max(runtime.NumCPU(), 1)
	}
	if limits.MemoryBytes < 0 {
		return Profile{}, errors.New("detected memory limit cannot be negative")
	}

	name := requested
	if requested == ProfileAuto {
		name = ProfileStandard
		if limits.CPUs <= 1 || limits.MemoryBytes > 0 && limits.MemoryBytes <= 3*gibibyte {
			name = ProfileNano
		}
	}

	profile := Profile{
		Name:                name,
		DetectedCPUs:        limits.CPUs,
		DetectedMemoryBytes: limits.MemoryBytes,
	}
	switch name {
	case ProfileNano:
		profile.GoMemoryLimitBytes = nanoMemoryLimit(limits.MemoryBytes)
		profile.SQLiteReadConnections = min(max(limits.CPUs, 1), 2)
		profile.SQLiteCacheKiB = 2 * 1024
		profile.SQLiteMmapBytes = 32 * mebibyte
		profile.SQLiteTempStore = "FILE"
		profile.PingWorkers = min(max(limits.CPUs*2, 2), 4)
		profile.PingQueueCapacity = 512
	case ProfileStandard:
		profile.GoMemoryLimitBytes = standardMemoryLimit(limits.MemoryBytes)
		profile.SQLiteReadConnections = min(max(limits.CPUs, 2), 8)
		profile.SQLiteCacheKiB = 8 * 1024
		profile.SQLiteMmapBytes = 128 * mebibyte
		profile.SQLiteTempStore = "MEMORY"
		profile.PingWorkers = min(max(limits.CPUs*2, 4), 16)
		profile.PingQueueCapacity = 2_048
	case ProfileScale:
		profile.SQLiteReadConnections = min(max(limits.CPUs, 4), 8)
		profile.SQLiteCacheKiB = 8 * 1024
		profile.SQLiteMmapBytes = 256 * mebibyte
		profile.SQLiteTempStore = "MEMORY"
		profile.PingWorkers = min(max(limits.CPUs*2, 8), 32)
		profile.PingQueueCapacity = 4_096
	default:
		return Profile{}, fmt.Errorf("unknown KOMARI_PROFILE %q (expected auto, nano, standard or scale)", requested)
	}
	return profile, nil
}

func nanoMemoryLimit(memory int64) int64 {
	const (
		floor   = 96 * mebibyte
		ceiling = 256 * mebibyte
	)
	if memory <= 0 {
		return ceiling
	}
	return min(max(memory/5, floor), ceiling)
}

func standardMemoryLimit(memory int64) int64 {
	const (
		floor   = 256 * mebibyte
		ceiling = 2 * gibibyte
	)
	if memory <= 0 {
		return gibibyte
	}
	return min(max(memory*2/5, floor), ceiling)
}

func Current() (Profile, error) {
	currentOnce.Do(func() {
		current, currentErr = Resolve(os.Getenv("KOMARI_PROFILE"), DetectLimits())
	})
	return current, currentErr
}

// Apply publishes the resolved profile to the Go runtime. Explicit GOMEMLIMIT
// and GOMAXPROCS values always win over automatic tuning.
func Apply() (Profile, error) {
	profile, err := Current()
	if err != nil {
		return Profile{}, err
	}
	applyOnce.Do(func() {
		if os.Getenv("GOMEMLIMIT") == "" && profile.GoMemoryLimitBytes > 0 {
			debug.SetMemoryLimit(profile.GoMemoryLimitBytes)
		}
		if os.Getenv("GOMAXPROCS") == "" && profile.DetectedCPUs > 0 {
			runtime.GOMAXPROCS(profile.DetectedCPUs)
		}
	})
	return profile, nil
}

func DetectLimits() Limits {
	limits := Limits{CPUs: max(runtime.NumCPU(), 1)}
	if cpu := detectCPUQuota(); cpu > 0 && cpu < limits.CPUs {
		limits.CPUs = cpu
	}
	limits.MemoryBytes = detectMemoryLimit()
	return limits
}

func detectCPUQuota() int {
	if raw, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 && fields[0] != "max" {
			quota, quotaErr := strconv.ParseInt(fields[0], 10, 64)
			period, periodErr := strconv.ParseInt(fields[1], 10, 64)
			if quotaErr == nil && periodErr == nil && quota > 0 && period > 0 {
				return max(int(math.Ceil(float64(quota)/float64(period))), 1)
			}
		}
	}
	quota := readPositiveInt("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period := readPositiveInt("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quota > 0 && period > 0 {
		return max(int(math.Ceil(float64(quota)/float64(period))), 1)
	}
	return 0
}

func detectMemoryLimit() int64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || value == "max" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		// Kernels commonly expose an enormous sentinel for "unlimited".
		if err == nil && parsed > 0 && parsed < 1<<60 {
			return parsed
		}
	}
	return 0
}

func readPositiveInt(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
