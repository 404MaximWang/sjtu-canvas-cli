package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestCompareVersions pins the ordering contract: numeric per segment (so
// 0.10.0 beats 0.9.9), optional v prefix, and rejection of anything else
// instead of a guess.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name    string
		a, b    string
		want    int // sign of the result
		wantErr bool
	}{
		{"equal", "v0.4.0", "0.4.0", 0, false},
		{"older patch", "v0.4.0", "v0.4.1", -1, false},
		{"newer minor", "v0.5.0", "v0.4.9", 1, false},
		{"numeric not lexical", "v0.10.0", "v0.9.9", 1, false},
		{"major bump", "v1.0.0", "v0.99.99", 1, false},
		{"dev rejected", "dev", "v0.4.0", 0, true},
		{"short rejected", "v0.4", "v0.4.0", 0, true},
		{"suffix rejected", "v0.4.0-rc1", "v0.4.0", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compareVersions(tc.a, tc.b)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sign(got) != tc.want {
				t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// sign collapses an ordering result to -1, 0 or 1.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// TestFindExpectedHash tests extracting the expected SHA256 hash for a named tarball from checksums.txt.
func TestFindExpectedHash(t *testing.T) {
	checksums := `3f682946a44da5c5fa2e13e6be381f2c1c4deed5c47699f7203a47dd43c1d60f  sjtu-v0.4.0-linux-amd64.tar.gz
140d692754a8b2eb08b740cf7aff16811e33ced092b7c9004afe525d18c04423  sjtu-v0.4.0-darwin-arm64.tar.gz
`

	hash, err := findExpectedHash(checksums, "sjtu-v0.4.0-darwin-arm64.tar.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "140d692754a8b2eb08b740cf7aff16811e33ced092b7c9004afe525d18c04423"
	if hash != expected {
		t.Errorf("got hash %s, want %s", hash, expected)
	}

	_, err = findExpectedHash(checksums, "sjtu-v0.4.0-windows-amd64.tar.gz")
	if err == nil {
		t.Errorf("expected error for missing asset, got nil")
	}
}

// TestVerifyChecksum tests validating data against matching and mismatched SHA256 checksums.
func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello sjtu update")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	tarballName := "test.tar.gz"

	validChecksums := hash + "  " + tarballName + "\n"
	if err := verifyChecksum(data, tarballName, validChecksums); err != nil {
		t.Errorf("expected valid checksum, got error: %v", err)
	}

	invalidChecksums := "deadbeefdeadbeef  " + tarballName + "\n"
	if err := verifyChecksum(data, tarballName, invalidChecksums); err == nil {
		t.Errorf("expected checksum mismatch error, got nil")
	}
}

// TestExtractBinary tests extracting the 'sjtu' binary from an in-memory tar.gz archive.
func TestExtractBinary(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho sjtu\n")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	hdr := &tar.Header{
		Name: "sjtu-v0.4.0-linux-amd64/sjtu",
		Mode: 0o755,
		Size: int64(len(binaryContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(binaryContent); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	extracted, err := extractBinary(buf.Bytes())
	if err != nil {
		t.Fatalf("extractBinary error: %v", err)
	}
	if !bytes.Equal(extracted, binaryContent) {
		t.Errorf("extracted content %q, want %q", extracted, binaryContent)
	}
}

// TestReplaceExecutable tests atomically replacing an existing file with new binary data.
func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "sjtu")

	oldContent := []byte("old-binary")
	newContent := []byte("new-binary")

	if err := os.WriteFile(targetPath, oldContent, 0o755); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	if err := replaceExecutable(targetPath, newContent); err != nil {
		t.Fatalf("replaceExecutable error: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if !bytes.Equal(got, newContent) {
		t.Errorf("file content = %q, want %q", got, newContent)
	}

	// Verify temporary file is cleaned up.
	if _, err := os.Stat(targetPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporary file still exists after replace")
	}
}
