package rollout

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const CacheBytes = 64 << 20
const CacheFiles = 128

var pruneMu sync.Mutex
var lastPrune time.Time

// Only the application's hash-named caches are candidates, never source logs.
func PruneCache(dir, keep string) {
	pruneMu.Lock()
	defer pruneMu.Unlock()
	if time.Since(lastPrune) < time.Minute {
		return
	}
	lastPrune = time.Now()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type entry struct {
		path  string
		size  int64
		stamp time.Time
	}
	var files []entry
	var total int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) != 69 || !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".seen") || strings.HasSuffix(name, ".turn")) {
			continue
		}
		valid := true
		for _, c := range name[:64] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, entry{filepath.Join(dir, name), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].stamp.Before(files[j].stamp) })
	count := len(files)
	for _, f := range files {
		if count <= CacheFiles && total <= CacheBytes {
			break
		}
		if strings.TrimSuffix(f.path, filepath.Ext(f.path)) == strings.TrimSuffix(keep, filepath.Ext(keep)) {
			continue
		}
		if os.Remove(f.path) == nil {
			count--
			total -= f.size
		}
	}
}
