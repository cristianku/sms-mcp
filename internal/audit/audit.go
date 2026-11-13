// Package audit writes an append-only JSON-lines log of every send attempt.
package audit

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Result values used in records.
const (
	ResultPending = "pending" // written before the request is handed to the phone
	ResultSent    = "sent"
	ResultRefused = "refused" // rejected by sms-mcp policy or validation
	ResultError   = "error"   // the phone or Google returned an error
)

// Record is one line in the audit log.
type Record struct {
	Time           time.Time `json:"time"`
	ID             string    `json:"id"`
	Tool           string    `json:"tool"`
	Target         string    `json:"target"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Length         int       `json:"length"`
	Result         string    `json:"result"`
	Error          string    `json:"error,omitempty"`
	Text           *string   `json:"text,omitempty"`
}

// Logger appends records to a file opened with O_APPEND and mode 0600.
type Logger struct {
	mu      sync.Mutex
	f       *os.File
	logText bool
	now     func() time.Time
}

// Open opens (creating if needed) the audit log at path. If the file exists
// with permissions wider than 0600 they are tightened and warn is called.
func Open(path string, logText bool, warn func(string)) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening audit log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("audit log %s has mode %o and chmod failed: %w", path, st.Mode().Perm(), err)
		}
		if warn != nil {
			warn(fmt.Sprintf("audit log %s had mode %s, changed to 0600", path, fs.FileMode(st.Mode().Perm())))
		}
	}
	return &Logger{f: f, logText: logText, now: time.Now}, nil
}

// LogsText reports whether message text is included in records.
func (l *Logger) LogsText() bool { return l.logText }

// Write appends rec. Time is filled in when zero. Text is dropped unless the
// logger was opened with logText. Each record is written with a single
// write(2) call and synced to disk.
func (l *Logger) Write(rec Record, text string) error {
	if rec.Time.IsZero() {
		rec.Time = l.now().UTC()
	}
	if l.logText {
		rec.Text = &text
	} else {
		rec.Text = nil
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(line); err != nil {
		return fmt.Errorf("writing audit log: %w", err)
	}
	return l.f.Sync()
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
