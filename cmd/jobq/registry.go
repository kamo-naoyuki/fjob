package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type serverRecord struct {
	BaseDir   string `json:"base_dir"`
	Socket    string `json:"socket"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	LastSeen  string `json:"last_seen"`
}

func resolveMasterDir(cliMasterDir string) (string, error) {
	if cliMasterDir != "" {
		return cliMasterDir, nil
	}
	if value := os.Getenv("JOBQ_MASTERDIR"); value != "" {
		return value, nil
	}
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return filepath.Join(value, "jobq", "master"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "jobq", "master"), nil
}

func serverRecordPath(masterDir, baseDir string) string {
	sum := sha256.Sum256([]byte(baseDir))
	return filepath.Join(masterDir, hex.EncodeToString(sum[:])[:16]+".json")
}

func registerServer(masterDir string, record serverRecord) error {
	if err := os.MkdirAll(masterDir, 0o755); err != nil {
		return err
	}
	return writeJSON(serverRecordPath(masterDir, record.BaseDir), record)
}

func unregisterServer(masterDir, baseDir string) error {
	err := os.Remove(serverRecordPath(masterDir, baseDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func touchServerRecord(masterDir, baseDir string) error {
	path := serverRecordPath(masterDir, baseDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var record serverRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	record.LastSeen = nowRFC3339()
	return writeJSON(path, record)
}

func listServers(masterDir string) ([]serverRecord, error) {
	entries, err := os.ReadDir(masterDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	servers := make([]serverRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(masterDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record serverRecord
		if json.Unmarshal(data, &record) != nil || record.BaseDir == "" {
			_ = os.Remove(path)
			continue
		}
		response, err := sendServerRequest(record.BaseDir, serverRequest{Op: "ping"})
		if err != nil || !response.OK || response.PID != record.PID {
			_ = os.Remove(path)
			continue
		}
		record.LastSeen = nowRFC3339()
		_ = writeJSON(path, record)
		servers = append(servers, record)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].BaseDir < servers[j].BaseDir })
	return servers, nil
}

func formatServerList(servers []serverRecord) string {
	if len(servers) == 0 {
		return "no running servers"
	}
	lines := make([]string, 0, len(servers)+1)
	lines = append(lines, fmt.Sprintf("%-8s %-24s %s", "PID", "LAST_SEEN", "BASE_DIR"))
	for _, server := range servers {
		lines = append(lines, fmt.Sprintf("%-8d %-24s %s", server.PID, server.LastSeen, server.BaseDir))
	}
	return strings.Join(lines, "\n")
}
