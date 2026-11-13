package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderQR(t *testing.T) {
	var b bytes.Buffer
	if err := renderQR(&b, "https://example.com/pair#fake", false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) < 10 || !strings.ContainsAny(b.String(), "▀▄") {
		t.Fatalf("unexpected QR output (%d lines)", len(lines))
	}
}

func TestWriteQRPNGMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "qr.png")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeQRPNG(p, "https://example.com/pair#fake"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || st.Size() == 0 {
		t.Fatalf("mode %v size %d", st.Mode().Perm(), st.Size())
	}
	data, _ := os.ReadFile(p)
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatal("not a PNG")
	}
}
