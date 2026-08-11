package cli

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/steipete/sonoscli/internal/sonos"
)

func TestStoreNameCompletions_WritesPrivateFile(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", cacheDir)

	if err := storeNameCompletions(time.Now(), []string{"Kitchen"}); err != nil {
		t.Fatalf("store cache: %v", err)
	}

	path, err := nameCompletionCachePath()
	if err != nil {
		t.Fatalf("cache path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}

	// Best-effort permission assertion: on Unix-like systems the cache should not be readable by group/other.
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("cache file perms = %v, want no group/other bits", info.Mode().Perm())
		}
	}
}

func TestCachedTopologyFootholdIP_UsesDiscoveredSpeakers(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", cacheDir)

	now := time.Now()
	if err := storeDiscoveredDevices(now, []sonos.Device{
		{Name: "Kitchen", IP: "192.168.1.11"},
		{Name: "Office", IP: "192.168.1.20"},
		{Name: "Invalid", IP: "not-an-ip"},
	}); err != nil {
		t.Fatalf("store discovered devices: %v", err)
	}

	if ip, ok := cachedTopologyFootholdIP(now, "office"); !ok || ip != "192.168.1.20" {
		t.Fatalf("cached office foothold = %q, %v", ip, ok)
	}
	if ip, ok := cachedTopologyFootholdIP(now, "New Room"); !ok || ip != "192.168.1.11" {
		t.Fatalf("cached fallback foothold = %q, %v", ip, ok)
	}
	if _, ok := cachedTopologyFootholdIP(now.Add(discoveryFootholdCacheTTL+time.Second), "Office"); ok {
		t.Fatal("expected expired foothold to miss")
	}
}

func TestCachedTopologyFootholdIP_LegacyNamesOnlyCacheMisses(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", cacheDir)

	now := time.Now()
	if err := storeNameCompletions(now, []string{"Office"}); err != nil {
		t.Fatalf("store names: %v", err)
	}
	if _, ok := cachedTopologyFootholdIP(now, "Office"); ok {
		t.Fatal("expected names-only cache to miss for topology foothold")
	}
	if names, ok := cachedNameCompletions(now); !ok || len(names) != 1 || names[0] != "Office" {
		t.Fatalf("legacy completion cache = %#v, %v", names, ok)
	}
}
