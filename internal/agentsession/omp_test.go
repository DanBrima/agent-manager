package agentsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeOmpSession writes a session file the way omp 18 does: a fixed-width
// title slot record, then the session header, then the conversation.
func writeOmpSession(t *testing.T, dir, id, cwd string, created, modified time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	title, err := json.Marshal(map[string]any{"type": "title", "v": 1, "title": "", "updatedAt": created.Format(time.RFC3339Nano), "pad": strings.Repeat(" ", 120)})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": id, "timestamp": created.UTC().Format("2006-01-02T15:04:05.000Z"), "cwd": cwd})
	if err != nil {
		t.Fatal(err)
	}
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(created.UTC().Format("2006-01-02T15:04:05.000Z"))
	path := filepath.Join(dir, stamp+"_"+id+".jsonl")
	body := string(title) + "\n" + string(header) + "\n" + `{"type":"message","message":{"role":"assistant"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOmpCaptureAndRecapture(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	root := ompRoot()
	if root != filepath.Join(agentDir, "sessions") {
		t.Fatalf("ompRoot = %q", root)
	}
	cwd := t.TempDir()
	here := filepath.Join(root, "-project")
	now := time.Now().Truncate(time.Millisecond)
	const (
		oldID    = "01a0e200-0000-7000-8000-000000000001"
		otherID  = "01a0e200-0000-7000-8000-000000000002"
		childID  = "01a0e200-0000-7000-8000-000000000003"
		firstID  = "01a0e200-0000-7000-8000-000000000004"
		secondID = "01a0e200-0000-7000-8000-000000000005"
	)
	writeOmpSession(t, here, oldID, cwd, now.Add(-time.Hour), now)
	writeOmpSession(t, filepath.Join(root, "-elsewhere"), otherID, t.TempDir(), now, now)
	// a subagent log in a session's artifact directory shares the cwd
	writeOmpSession(t, filepath.Join(here, "2026-09-27T09-44-11-661Z_"+oldID), childID, cwd, now, now)
	if id, ok := Capture("omp", cwd, now, nil); ok {
		t.Fatalf("captured stale, foreign, or subagent session %q", id)
	}
	path := writeOmpSession(t, here, firstID, cwd, now, now)
	writeOmpSession(t, here, secondID, cwd, now.Add(time.Second), now.Add(time.Second))
	if id, ok := Capture("omp", cwd, now, nil); !ok || id != firstID {
		t.Fatalf("Capture = %q, %v", id, ok)
	}
	if id, ok := Capture("omp", cwd, now, map[string]bool{firstID: true}); !ok || id != secondID {
		t.Fatalf("claimed Capture = %q, %v", id, ok)
	}
	snapshot, ok := Snapshot("omp", cwd)
	if !ok || len(snapshot) != 3 {
		t.Fatalf("Snapshot = %v, %v", snapshot, ok)
	}
	if id, ok := Recapture("omp", cwd, snapshot, nil); ok {
		t.Fatalf("Recapture without new activity = %q", id)
	}
	later := now.Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if id, ok := Recapture("omp", cwd, snapshot, nil); !ok || id != firstID {
		t.Fatalf("Recapture = %q, %v", id, ok)
	}
}

func TestOmpCaptureResolvesSymlinkedCwd(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	now := time.Now().Truncate(time.Millisecond)
	const id = "01a0e200-0000-7000-8000-00000000000a"
	writeOmpSession(t, filepath.Join(ompRoot(), "-real"), id, resolvePath(real), now, now)
	if got, ok := Capture("omp", link, now, nil); !ok || got != id {
		t.Fatalf("Capture via symlink = %q, %v", got, ok)
	}
}

func TestOmpMetaSkipsMalformedHeaders(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no-header":  `{"type":"title","v":1}` + "\n" + `{"type":"message"}` + "\n",
		"bad-id":     `{"type":"title","v":1}` + "\n" + `{"type":"session","id":"../x","timestamp":"2026-09-27T09:44:11.661Z","cwd":"/p"}` + "\n",
		"bad-stamp":  `{"type":"title","v":1}` + "\n" + `{"type":"session","id":"abc","timestamp":"yesterday","cwd":"/p"}` + "\n",
		"no-cwd":     `{"type":"title","v":1}` + "\n" + `{"type":"session","id":"abc","timestamp":"2026-09-27T09:44:11.661Z"}` + "\n",
		"not-json":   "garbage\nmore garbage\n",
		"empty-file": "",
	} {
		path := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		id, cwd, _, err := ompMeta(path)
		if err != nil || id != "" || cwd != "" {
			t.Fatalf("%s: ompMeta = %q, %q, %v", name, id, cwd, err)
		}
	}
	path := filepath.Join(dir, "header-first.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session","id":"abc","timestamp":"2026-09-27T09:44:11.661Z","cwd":"/p"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, cwd, _, err := ompMeta(path); err != nil || id != "abc" || cwd != "/p" {
		t.Fatalf("header on line 1: ompMeta = %q, %q, %v", id, cwd, err)
	}
}
