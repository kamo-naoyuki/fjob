package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type runLocation struct {
	BaseDir     string `json:"base_dir"`
	ProjectName string `json:"project_name"`
	RunID       string `json:"run_id"`
}

func registerRun(paths pathSet, runID string) error {
	return registerRunLocation(runLocation{
		BaseDir: paths.baseDir, ProjectName: paths.queueName, RunID: runID,
	})
}

func registerRunLocation(location runLocation) error {
	baseDir, err := filepath.Abs(location.BaseDir)
	if err != nil {
		return err
	}
	location.BaseDir = baseDir
	dir, err := runRegistryDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := runLocationPath(dir, location.RunID)
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(path); err == nil {
		var existing runLocation
		if json.Unmarshal(data, &existing) != nil || existing != location {
			return fmt.Errorf("run id %q is already registered to another location", location.RunID)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeJSON(path, location)
}

func unregisterRun(runID string) error {
	dir, err := runRegistryDir()
	if err != nil {
		return err
	}
	path, err := runLocationPath(dir, runID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func resolveRunLocation(runID string) (runLocation, bool, error) {
	dir, err := runRegistryDir()
	if err != nil {
		return runLocation{}, false, err
	}
	path, err := runLocationPath(dir, runID)
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
		return runLocation{}, false, fmt.Errorf("invalid run registry entry for %q: %w", runID, err)
	}
	if location.BaseDir == "" || location.ProjectName == "" || location.RunID != runID {
		return runLocation{}, false, fmt.Errorf("invalid run registry entry for %q", runID)
	}
	return location, true, nil
}

func resolveExistingRunTarget(cliBaseDir, cliProjectName, runID string) (string, string, error) {
	if runID != "" {
		location, found, err := resolveRunLocation(runID)
		if err != nil {
			return "", "", err
		}
		if found {
			if cliBaseDir != "" {
				baseDir, err := filepath.Abs(cliBaseDir)
				if err != nil {
					return "", "", err
				}
				if baseDir != location.BaseDir {
					return "", "", fmt.Errorf("run %q is registered under basedir %q, not %q", runID, location.BaseDir, baseDir)
				}
			} else {
				cliBaseDir = location.BaseDir
			}
			if cliProjectName != "" && cliProjectName != location.ProjectName {
				return "", "", fmt.Errorf("run %q is registered under project %q, not %q", runID, location.ProjectName, cliProjectName)
			}
			if cliProjectName == "" {
				cliProjectName = location.ProjectName
			}
		}
	}
	baseDir, _, err := resolveBaseDir(cliBaseDir)
	if err != nil {
		return "", "", err
	}
	projectName, err := resolveProjectName(baseDir, cliProjectName)
	if err != nil {
		return "", "", err
	}
	return baseDir, projectName, nil
}

func runRegistryDir() (string, error) {
	masterDir, err := resolveMasterDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(masterDir, "runs"), nil
}

func runLocationPath(dir, runID string) (string, error) {
	if runID == "" || filepath.Base(runID) != runID {
		return "", fmt.Errorf("invalid run id %q", runID)
	}
	return filepath.Join(dir, runID+".json"), nil
}
