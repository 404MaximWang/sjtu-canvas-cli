package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Version is the single version declaration of the sjtu binary. Release
// builds inject the git tag via -ldflags "-X .../cli.Version=<tag>"
// (see .github/workflows/release.yml); everything else reports "dev".
var Version = "dev"

// releaseRepo is the GitHub repository hosting the release artifacts.
const releaseRepo = "404MaximWang/sjtu-canvas-cli"

// newUpdateCmd constructs the 'sjtu update' command for binary self-updating.
func newUpdateCmd(stdout, stderr io.Writer) *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update sjtu binary to the latest GitHub release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), checkOnly, stdout, stderr)
		},
	}

	cmd.Flags().BoolVarP(&checkOnly, "check", "c", false, "check for updates without installing")
	return cmd
}

// runUpdate executes the update check and self-update process.
func runUpdate(ctx context.Context, checkOnly bool, stdout, stderr io.Writer) error {
	fmt.Fprintln(stderr, "Checking for latest release...")
	latestTag, err := fetchLatestTag(ctx)
	if err != nil {
		return fmt.Errorf("check latest version: %w", err)
	}

	cmp, err := compareVersions(Version, latestTag)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "warning: local version %q is not a release build; it will be replaced by %s\n", Version, latestTag)
	case cmp == 0:
		fmt.Fprintf(stdout, "sjtu is already up to date (%s).\n", Version)
		return nil
	case cmp > 0:
		fmt.Fprintf(stdout, "local version %s is newer than the latest release %s; nothing to do.\n", Version, latestTag)
		return nil
	}

	fmt.Fprintf(stdout, "New version available: %s (current: %s)\n", latestTag, Version)
	if checkOnly {
		return nil
	}

	tarballName := fmt.Sprintf("sjtu-%s-%s-%s.tar.gz", latestTag, runtime.GOOS, runtime.GOARCH)

	fmt.Fprintf(stderr, "Downloading %s...\n", tarballName)
	tarballBytes, err := downloadAsset(ctx, latestTag, tarballName)
	if err != nil {
		return fmt.Errorf("download %s: %w", tarballName, err)
	}

	fmt.Fprintln(stderr, "Downloading checksums.txt...")
	checksumsBytes, err := downloadAsset(ctx, latestTag, "checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}

	fmt.Fprintln(stderr, "Verifying checksum...")
	if err := verifyChecksum(tarballBytes, tarballName, string(checksumsBytes)); err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}

	fmt.Fprintln(stderr, "Extracting binary...")
	newBinary, err := extractBinary(tarballBytes)
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		realPath = exePath
	}

	fmt.Fprintf(stderr, "Replacing %s...\n", realPath)
	if err := replaceExecutable(realPath, newBinary); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Successfully updated sjtu to %s!\n", latestTag)
	return nil
}

// compareVersions orders two version strings of the form [v]MAJOR.MINOR.PATCH:
// negative when a < b, zero when equal, positive when a > b. Anything not in
// that shape ("dev", commit hashes) is an error, not a guess.
func compareVersions(a, b string) (int, error) {
	parse := func(s string) ([3]int, error) {
		var out [3]int
		parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
		if len(parts) != 3 {
			return out, fmt.Errorf("not a vMAJOR.MINOR.PATCH version: %q", s)
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return out, fmt.Errorf("not a vMAJOR.MINOR.PATCH version: %q", s)
			}
			out[i] = n
		}
		return out, nil
	}
	va, err := parse(a)
	if err != nil {
		return 0, err
	}
	vb, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] - vb[i], nil
		}
	}
	return 0, nil
}

// fetchLatestTag resolves the latest release tag from GitHub without hitting API rate limits.
func fetchLatestTag(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/latest", releaseRepo)
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		return "", fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no Location header returned from %s", url)
	}

	parts := strings.Split(strings.TrimRight(loc, "/"), "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid Location URL: %s", loc)
	}
	tag := parts[len(parts)-1]
	if tag == "" || tag == "latest" || tag == "releases" {
		return "", fmt.Errorf("failed to parse tag from Location: %s", loc)
	}
	return tag, nil
}

// downloadAsset fetches one release asset file by tag and filename.
func downloadAsset(ctx context.Context, tag, filename string) ([]byte, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", releaseRepo, tag, filename)
	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// findExpectedHash parses checksums.txt content and extracts the expected hash for tarballName.
func findExpectedHash(checksumsContent, tarballName string) (string, error) {
	for _, line := range strings.Split(checksumsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == tarballName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found in checksums.txt", tarballName)
}

// verifyChecksum checks the SHA256 digest of tarballData against
// checksums.txt. Both files come from the same GitHub release, so this
// guards integrity (corrupt or truncated downloads), not authenticity: a
// compromised release could replace both. Releases are not signed.
func verifyChecksum(tarballData []byte, tarballName, checksumsContent string) error {
	expected, err := findExpectedHash(checksumsContent, tarballName)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(tarballData)
	actual := hex.EncodeToString(sum[:])

	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// extractBinary decompresses a tar.gz payload and returns the 'sjtu' binary bytes.
func extractBinary(tarballData []byte) ([]byte, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(tarballData))
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		if header.Typeflag == tar.TypeReg && (header.Name == "sjtu" || strings.HasSuffix(header.Name, "/sjtu")) {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary 'sjtu' not found in tarball")
}

// replaceExecutable writes newBinary to a temporary file and atomically renames it over the target executable.
func replaceExecutable(targetPath string, newBinary []byte) error {
	tmpPath := targetPath + ".tmp"
	if err := os.WriteFile(tmpPath, newBinary, 0o755); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied writing to %s (try running with sudo)", tmpPath)
		}
		return fmt.Errorf("write temporary binary: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied replacing %s (try running with sudo)", targetPath)
		}
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
