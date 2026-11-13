package gm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// sessionFile is the on-disk format of the session file.
type sessionFile struct {
	Version  int             `json:"version"`
	PairedAt time.Time       `json:"paired_at"`
	PhoneID  string          `json:"phone_id,omitempty"`
	AuthData *libgm.AuthData `json:"auth_data"`
}

const sessionVersion = 1

// Session is a paired Google Messages session.
type Session struct {
	PairedAt time.Time
	PhoneID  string
	AuthData *libgm.AuthData
}

// SessionStore reads and writes the session file. The file holds the
// encryption keys and tokens of the paired session, so it is always written
// with mode 0600.
type SessionStore struct {
	Path string
	// Warn is called for non-fatal problems such as fixed permissions.
	Warn func(string)
}

// ErrNoSession is returned by Load when the session file does not exist.
var ErrNoSession = errors.New("no session file: run `sms-mcp pair` first")

// Load reads the session. If the file is readable by group or others its
// mode is tightened to 0600 and a warning is emitted.
func (s *SessionStore) Load() (*Session, error) {
	st, err := os.Stat(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSession
	} else if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("session file %s is not a regular file", s.Path)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		if err := os.Chmod(s.Path, 0o600); err != nil {
			return nil, fmt.Errorf("session file %s has mode %o and chmod 600 failed: %w", s.Path, perm, err)
		}
		s.warn(fmt.Sprintf("session file %s had mode %s, changed to 0600; if other users could read it, unpair the device in Google Messages and pair again", s.Path, fs.FileMode(perm)))
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	var sf sessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parsing session file: %w", err)
	}
	if sf.Version != sessionVersion {
		return nil, fmt.Errorf("unsupported session file version %d", sf.Version)
	}
	if sf.AuthData == nil || sf.AuthData.Browser == nil {
		return nil, fmt.Errorf("session file %s does not contain a paired session: run `sms-mcp pair`", s.Path)
	}
	return &Session{PairedAt: sf.PairedAt, PhoneID: sf.PhoneID, AuthData: sf.AuthData}, nil
}

// Marshal serialises a session. It holds the AuthData cookie lock while
// encoding because libgm updates cookies concurrently.
func Marshal(sess *Session) ([]byte, error) {
	if sess == nil || sess.AuthData == nil {
		return nil, errors.New("nil session")
	}
	sess.AuthData.CookiesLock.RLock()
	defer sess.AuthData.CookiesLock.RUnlock()
	return json.MarshalIndent(sessionFile{
		Version:  sessionVersion,
		PairedAt: sess.PairedAt,
		PhoneID:  sess.PhoneID,
		AuthData: sess.AuthData,
	}, "", "  ")
}

// Save atomically writes the session (temp file with 0600, fsync, rename).
func (s *SessionStore) Save(sess *Session) error {
	data, err := Marshal(sess)
	if err != nil {
		return err
	}
	return s.writeAtomic(data)
}

// SaveIfChanged writes the session only if its serialisation differs from
// prev and returns the new serialisation.
func (s *SessionStore) SaveIfChanged(sess *Session, prev []byte) ([]byte, error) {
	data, err := Marshal(sess)
	if err != nil {
		return prev, err
	}
	if bytes.Equal(data, prev) {
		return prev, nil
	}
	if err := s.writeAtomic(data); err != nil {
		return prev, err
	}
	return data, nil
}

func (s *SessionStore) writeAtomic(data []byte) error {
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp session file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

func (s *SessionStore) warn(msg string) {
	if s.Warn != nil {
		s.Warn(msg)
	}
}
