package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// processFile decides what to do with one File entry and either performs
// the work or, when dryRun is true, only prints the intended action.
//
// The decision is:
//   - if Dest exists and its sha256 matches f.SHA256 -> present, no work
//   - otherwise -> fetch
//
// Fetching is atomic: the download goes to a sibling tmp file and is
// renamed onto Dest only after the sha256 check passes. A crash mid-fetch
// leaves at most a stale `Dest.tmp.<pid>` next to Dest; Dest itself stays
// either absent or in its previous good state. A sha256 mismatch after
// download deletes the tmp file and returns an error; Dest is not touched.
func processFile(f File, dryRun bool) error {
	want := strings.ToLower(f.SHA256)

	ok, err := verifyExisting(f.Dest, want)
	if err != nil {
		return err
	}
	if ok {
		fmt.Printf("present: %s (sha256 matches)\n", f.Dest)
		return nil
	}

	fmt.Printf("fetch:   %s <- %s\n", f.Dest, f.URL)
	if dryRun {
		return nil
	}

	return doFetch(f, want)
}

func doFetch(f File, want string) error {
	if err := os.MkdirAll(filepath.Dir(f.Dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d", f.Dest, os.Getpid())
	gotHex, err := downloadTo(f.URL, tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}

	if gotHex != want {
		os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch for %s: want %s, got %s", f.URL, want, gotHex)
	}

	if err := os.Rename(tmp, f.Dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, f.Dest, err)
	}

	return nil
}

// verifyExisting returns true if `path` exists and its sha256 equals `want`
// (a lowercased hex string). Returns false if the file does not exist or
// its sha256 differs.
func verifyExisting(path, want string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s is a directory", path)
	}

	got, err := sha256File(path)
	if err != nil {
		return false, err
	}
	return got == want, nil
}

// downloadTo streams `url` into a newly created file at `dest` and returns
// the lowercased hex sha256 of the bytes that were written.
func downloadTo(url, dest string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http get %s: status %s", url, resp.Status)
	}

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}
	defer out.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	if err := out.Sync(); err != nil {
		return "", fmt.Errorf("sync %s: %w", dest, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
