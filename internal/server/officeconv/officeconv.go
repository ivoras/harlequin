// Package officeconv upgrades legacy/OpenDocument Office files (.doc .xls .ppt
// .ods) to their modern OOXML equivalents by running LibreOffice headless on the
// host, so the regular DOCX/XLSX/PPTX paths (docling or the built-in readers)
// can ingest them. docling-serve needs LibreOffice for these formats and its
// container usually does not ship it.
package officeconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnavailable means LibreOffice (soffice) is not installed on this host.
var ErrUnavailable = errors.New("officeconv: soffice not installed")

// modern maps a legacy extension to the OOXML extension to convert it to.
var modern = map[string]string{".doc": "docx", ".xls": "xlsx", ".ppt": "pptx", ".ods": "xlsx"}

const (
	timeout   = 2 * time.Minute
	maxOutput = 256 << 20
)

// Handles reports whether name is a format this package converts.
func Handles(name string) bool { _, ok := modern[strings.ToLower(filepath.Ext(name))]; return ok }

// ToModern converts data (named name) and returns the new bytes and file name.
// Each call uses its own temp dir and LibreOffice profile, so calls run in
// parallel and nothing is shared between uploads. The file is untrusted input:
// the run is time-limited and works in a throwaway directory.
func ToModern(ctx context.Context, name string, data []byte) ([]byte, string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	target, ok := modern[ext]
	if !ok {
		return nil, "", fmt.Errorf("officeconv: unsupported extension %q", ext)
	}
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		return nil, "", ErrUnavailable
	}
	dir, err := os.MkdirTemp("", "officeconv-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in"+ext) // fixed name: the upload's own name is never a path
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, "", err
	}
	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, soffice, "-env:UserInstallation=file://"+filepath.Join(dir, "profile"),
		"--headless", "--norestore", "--convert-to", target, "--outdir", out, in)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("officeconv: soffice failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	res := filepath.Join(out, "in."+target)
	if st, err := os.Stat(res); err != nil || st.Size() == 0 || st.Size() > maxOutput {
		return nil, "", errors.New("officeconv: soffice produced no usable output (corrupt or password-protected file?)")
	}
	b, err := os.ReadFile(res)
	if err != nil {
		return nil, "", err
	}
	return b, strings.TrimSuffix(name, filepath.Ext(name)) + "." + target, nil
}
