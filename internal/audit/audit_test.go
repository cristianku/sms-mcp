package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestWriteWithoutText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Write(Record{ID: "a", Tool: "send_sms", Target: "+15550100123", Length: 5, Result: ResultSent}, "hello"); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "hello") {
		t.Fatal("text must not be logged when logText is false")
	}
	recs := readRecords(t, path)
	if len(recs) != 1 || recs[0]["result"] != "sent" || recs[0]["target"] != "+15550100123" || recs[0]["time"] == "" {
		t.Fatalf("records: %v", recs)
	}
}

func TestAppendAndText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Write(Record{ID: "1", Result: ResultPending}, "first")
	_ = l.Close()
	// Reopen: must append, not truncate.
	l, err = Open(path, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Write(Record{ID: "1", Result: ResultSent}, "first")
	_ = l.Close()
	recs := readRecords(t, path)
	if len(recs) != 2 || recs[0]["text"] != "first" || recs[1]["result"] != "sent" {
		t.Fatalf("records: %v", recs)
	}
}

func TestFixesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var warned bool
	l, err := Open(path, false, func(string) { warned = true })
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 || !warned {
		t.Fatalf("mode %o warned %v", st.Mode().Perm(), warned)
	}
}
