package cli

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steipete/sonoscli/internal/sonos"
)

const (
	nameCompletionCacheTTL    = 30 * time.Second
	discoveryFootholdCacheTTL = 5 * time.Minute
)

type cachedSpeaker struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type nameCompletionCacheFile struct {
	UpdatedAt time.Time       `json:"updatedAt"`
	Names     []string        `json:"names"`
	Speakers  []cachedSpeaker `json:"speakers,omitempty"`
}

func readNameCompletionCacheFile() (nameCompletionCacheFile, bool) {
	path, err := nameCompletionCachePath()
	if err != nil {
		return nameCompletionCacheFile{}, false
	}

	raw, err := os.ReadFile(path) //nolint:gosec // Cache path is derived from the user's config directory.
	if err != nil {
		return nameCompletionCacheFile{}, false
	}
	// Avoid large reads if the cache ever gets corrupted.
	if len(raw) > 64*1024 {
		return nameCompletionCacheFile{}, false
	}

	var cache nameCompletionCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil {
		return nameCompletionCacheFile{}, false
	}
	if cache.UpdatedAt.IsZero() || len(cache.Names) == 0 {
		return nameCompletionCacheFile{}, false
	}

	return cache, true
}

func cachedNameCompletions(now time.Time) ([]string, bool) {
	cache, ok := readNameCompletionCacheFile()
	if !ok {
		return nil, false
	}
	if now.Sub(cache.UpdatedAt) > nameCompletionCacheTTL {
		return nil, false
	}
	return cache.Names, true
}

func cachedTopologyFootholdIP(now time.Time, preferredName string) (string, bool) {
	cache, ok := readNameCompletionCacheFile()
	if !ok || now.Sub(cache.UpdatedAt) > discoveryFootholdCacheTTL {
		return "", false
	}

	preferredName = strings.TrimSpace(preferredName)
	fallbackIP := ""
	for _, speaker := range cache.Speakers {
		name := strings.TrimSpace(speaker.Name)
		ip := strings.TrimSpace(speaker.IP)
		if name == "" || net.ParseIP(ip) == nil {
			continue
		}
		if fallbackIP == "" {
			fallbackIP = ip
		}
		if name == preferredName {
			return ip, true
		}
	}
	for _, speaker := range cache.Speakers {
		name := strings.TrimSpace(speaker.Name)
		ip := strings.TrimSpace(speaker.IP)
		if name != "" && net.ParseIP(ip) != nil && strings.EqualFold(name, preferredName) {
			return ip, true
		}
	}
	return fallbackIP, fallbackIP != ""
}

func storeNameCompletions(now time.Time, names []string) error {
	if len(names) == 0 {
		return errors.New("no names")
	}
	return writeNameCompletionCache(nameCompletionCacheFile{
		UpdatedAt: now,
		Names:     names,
	})
}

func storeDiscoveredDevices(now time.Time, devices []sonos.Device) error {
	names := extractDeviceNames(devices)
	if len(names) == 0 {
		return errors.New("no names")
	}

	speakers := make([]cachedSpeaker, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		name := strings.TrimSpace(device.Name)
		ip := strings.TrimSpace(device.IP)
		if name == "" || net.ParseIP(ip) == nil {
			continue
		}
		key := strings.ToLower(name) + "\x00" + ip
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		speakers = append(speakers, cachedSpeaker{Name: name, IP: ip})
	}
	sort.Slice(speakers, func(i, j int) bool {
		if speakers[i].Name == speakers[j].Name {
			return speakers[i].IP < speakers[j].IP
		}
		return speakers[i].Name < speakers[j].Name
	})

	return writeNameCompletionCache(nameCompletionCacheFile{
		UpdatedAt: now,
		Names:     names,
		Speakers:  speakers,
	})
}

func writeNameCompletionCache(cache nameCompletionCacheFile) error {
	path, err := nameCompletionCachePath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, "name-completions-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path) //nolint:gosec // Atomic rename stays inside the cache directory.
}

func nameCompletionCachePath() (string, error) {
	if override := os.Getenv("SONOSCLI_COMPLETION_CACHE_DIR"); override != "" {
		return filepath.Join(override, "sonoscli", "name-completions.json"), nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sonoscli", "name-completions.json"), nil
}
