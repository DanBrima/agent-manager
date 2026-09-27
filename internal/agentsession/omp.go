package agentsession

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ompRoot is where omp keeps one directory of session files per working
// directory: ~/.omp/agent/sessions, or $PI_CODING_AGENT_DIR/sessions.
func ompRoot() string {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		agentDir = filepath.Join(home, ".omp", "agent")
	}
	return filepath.Join(agentDir, "sessions")
}

func captureOmp(root, cwd string, launchedAt time.Time, claimed map[string]bool) (string, bool) {
	return captureCandidates(ompCandidates(root, cwd, launchedAt.Add(-clockSlack), claimed))
}

func snapshotOmp(root, cwd string) (map[string]int64, bool) {
	return snapshotCandidates(ompCandidates(root, cwd, time.Time{}, nil))
}

func recaptureOmp(root, cwd string, snapshot map[string]int64, claimed map[string]bool) []candidate {
	cands, err := ompCandidates(root, cwd, time.Time{}, claimed)
	return recaptureCandidates(cands, err, snapshot)
}

// ompCandidates reads the session files one level under each directory in
// root. The directory next to each file with the same stem holds that
// session's artifacts, subagent logs included, so it is not descended into.
// omp writes a session file only once the first reply lands, so a fresh
// launch has nothing to capture until then.
func ompCandidates(root, cwd string, cutoff time.Time, claimed map[string]bool) ([]candidate, error) {
	if root == "" {
		return nil, os.ErrNotExist
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	wantCwd := resolvePath(cwd)
	var cands []candidate
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, dir.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if info.ModTime().Before(cutoff) {
				continue
			}
			id, sessionCwd, created, err := ompMeta(filepath.Join(root, dir.Name(), entry.Name()))
			if err != nil {
				return nil, err
			}
			if id == "" || created.Before(cutoff) || resolvePath(sessionCwd) != wantCwd || claimed[id] {
				continue
			}
			cands = append(cands, candidate{id: id, modTime: info.ModTime()})
		}
	}
	return cands, nil
}

// ompMeta reads the session header. omp 18 opens every file with a fixed
// title slot record and writes the header second, so the first two lines
// are read and the one typed "session" wins.
func ompMeta(path string) (id, cwd string, created time.Time, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 2 && scanner.Scan(); i++ {
		var header struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Cwd       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &header) != nil || header.Type != "session" {
			continue
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, header.Timestamp)
		if parseErr != nil || !sessionIDPattern.MatchString(header.ID) || header.Cwd == "" {
			return "", "", time.Time{}, nil
		}
		return header.ID, header.Cwd, stamp, nil
	}
	return "", "", time.Time{}, scanErr(scanner)
}
