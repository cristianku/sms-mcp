package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// renderQR draws a QR code with Unicode half blocks, two modules per
// character row. Light modules are drawn as blocks, which scans correctly on
// the usual dark terminal background; invert for light backgrounds.
func renderQR(w io.Writer, content string, invert bool) error {
	q, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return err
	}
	bm := q.Bitmap() // true = dark module; includes the quiet zone
	light := func(y, x int) bool {
		if y >= len(bm) {
			return !invert
		}
		return bm[y][x] == invert
	}
	var b strings.Builder
	for y := 0; y < len(bm); y += 2 {
		for x := range bm[y] {
			top, bottom := light(y, x), light(y+1, x)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// writeQRPNG writes the QR code PNG with mode 0600: the payload contains the
// session's encryption keys.
func writeQRPNG(path, content string) error {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return err
	}
	png, err := q.PNG(512)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(png); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
