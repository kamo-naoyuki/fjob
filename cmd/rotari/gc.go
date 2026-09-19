package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const runRegistryGCCacheTTL = 10 * time.Minute

type runRegistryGCCache struct {
	CreatedAt string        `json:"created_at"`
	Entries   []runLocation `json:"entries"`
}

func cmdGC(args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	masterdir := cliString(fs, "masterdir", "")
	apply := fs.Bool("apply", false, "remove the cached orphan entries")
	if err := fs.Parse(args); err != nil || len(fs.Args()) != 0 {
		printError("usage: " + cliUsage("gc"))
		return 1
	}

	masterDir, err := resolveMasterDir(*masterdir)
	if err != nil {
		printError(err)
		return 1
	}
	if *apply {
		return applyRunRegistryGC(masterDir)
	}
	return scanRunRegistryGC(masterDir)
}

func scanRunRegistryGC(masterDir string) int {
	entries, err := orphanRunRegistryEntries(masterDir)
	if err != nil {
		printErrorf("failed to scan run registry: %v", err)
		return 1
	}
	cache := runRegistryGCCache{CreatedAt: nowRFC3339(), Entries: entries}
	cachePath := filepath.Join(masterDir, "gc.json")
	if err := os.MkdirAll(masterDir, 0o755); err != nil {
		printErrorf("failed to create master directory: %v", err)
		return 1
	}
	if err := writeJSON(cachePath, cache); err != nil {
		printErrorf("failed to save GC plan: %v", err)
		return 1
	}
	fmt.Printf("found %d orphan run registry entr%s\n", len(entries), pluralSuffix(len(entries)))
	for _, entry := range entries {
		fmt.Printf("  %s -> %s/projects/%s/runs/%s\n", entry.RunID, entry.BaseDir, entry.ProjectName, entry.RunID)
	}
	fmt.Printf("GC plan cached at %s (expires in %s)\n", cachePath, runRegistryGCCacheTTL)
	fmt.Printf("review the plan, then run: rotari gc --apply\n")
	return 0
}

func pluralSuffix(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}

func orphanRunRegistryEntries(masterDir string) ([]runLocation, error) {
	dir := filepath.Join(masterDir, "runs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	orphans := make([]runLocation, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var location runLocation
		if err := json.Unmarshal(data, &location); err != nil {
			continue
		}
		if !validRunRegistryLocation(location) || runLocationExists(location) {
			continue
		}
		orphans = append(orphans, location)
	}
	return orphans, nil
}

func validRunRegistryLocation(location runLocation) bool {
	return location.BaseDir != "" && filepath.IsAbs(location.BaseDir) &&
		location.ProjectName != "" && filepath.Base(location.ProjectName) == location.ProjectName &&
		location.ProjectName != "." && location.ProjectName != ".." &&
		location.RunID != "" && filepath.Base(location.RunID) == location.RunID &&
		location.RunID != "." && location.RunID != ".."
}

func runLocationExists(location runLocation) bool {
	_, err := os.Stat(filepath.Join(location.BaseDir, "projects", location.ProjectName, "runs", location.RunID))
	return err == nil
}

func applyRunRegistryGC(masterDir string) int {
	cachePath := filepath.Join(masterDir, "gc.json")
	data, err := os.ReadFile(cachePath)
	if os.IsNotExist(err) {
		printError("no cached GC plan; run 'rotari gc' first")
		return 1
	}
	if err != nil {
		printErrorf("failed to read GC plan: %v", err)
		return 1
	}
	var cache runRegistryGCCache
	if err := json.Unmarshal(data, &cache); err != nil {
		printErrorf("invalid GC plan: %v", err)
		return 1
	}
	createdAt, err := time.Parse(time.RFC3339, cache.CreatedAt)
	if err != nil || time.Since(createdAt) > runRegistryGCCacheTTL {
		printError("cached GC plan has expired; run 'rotari gc' again")
		return 1
	}

	removed := 0
	for _, planned := range cache.Entries {
		current, found, err := resolveRunLocationAt(masterDir, planned.RunID)
		if err != nil {
			printErrorf("failed to read registry entry %q: %v", planned.RunID, err)
			return 1
		}
		if !found || current != planned || runLocationExists(current) {
			continue
		}
		path, err := runLocationPath(filepath.Join(masterDir, "runs"), planned.RunID)
		if err != nil {
			printError(err)
			return 1
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			printErrorf("failed to remove registry entry %q: %v", planned.RunID, err)
			return 1
		}
		removed++
	}
	_ = os.Remove(cachePath)
	fmt.Printf("removed %d orphan run registry entr%s\n", removed, pluralSuffix(removed))
	return 0
}

func resolveRunLocationAt(masterDir, runID string) (runLocation, bool, error) {
	path, err := runLocationPath(filepath.Join(masterDir, "runs"), runID)
	if err != nil {
		return runLocation{}, false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return runLocation{}, false, nil
	}
	if err != nil {
		return runLocation{}, false, err
	}
	var location runLocation
	if err := json.Unmarshal(data, &location); err != nil {
		return runLocation{}, false, err
	}
	return location, true, nil
}
