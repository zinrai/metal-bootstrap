package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// processISO decides what to do with the iso: section of one target and
// either performs the work or, when dryRun is true, only prints the
// intended actions.
//
// Entries are grouped by `From` so that each ISO is mounted at most once
// per target. If every entry in a group can be skipped, the ISO is not
// mounted at all.
//
// Skip decision for one entry:
//   - Dest does not exist -> extract (mount required)
//   - Dest exists           -> present
//
// The integrity of the source ISO is the responsibility of files:, which
// runs earlier in the same run and verifies sha256 there. By the time
// processISO is called, the ISO referenced by `from:` is either:
//   - declared in files: and already verified by processFile, or
//   - placed by the operator outside this tool, in which case there is
//     no expected sha256 to check against.
//
// Either way, recomputing sha256 here would be redundant or impossible.
//
// Atomicity: each extracted file goes to a sibling tmp file and is
// renamed onto Dest after the copy completes.
func processISO(entries []ISOEntry, dryRun bool) error {
	groups := groupByISO(entries)

	for isoPath, group := range groups {
		if err := processGroup(isoPath, group, dryRun); err != nil {
			return err
		}
	}
	return nil
}

func groupByISO(entries []ISOEntry) map[string][]ISOEntry {
	m := make(map[string][]ISOEntry)
	for _, e := range entries {
		m[e.From] = append(m[e.From], e)
	}
	return m
}

func processGroup(isoPath string, entries []ISOEntry, dryRun bool) error {
	// Print decisions and collect the work that still needs the ISO
	// mounted.
	var pending []ISOEntry
	for _, e := range entries {
		exists, err := destExists(e.Dest)
		if err != nil {
			return err
		}
		if exists {
			fmt.Printf("present: %s\n", e.Dest)
			continue
		}
		fmt.Printf("extract: %s <- %s!%s\n", e.Dest, isoPath, e.Src)
		pending = append(pending, e)
	}

	if len(pending) == 0 || dryRun {
		return nil
	}

	return mountAndExtract(isoPath, pending)
}

func destExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

func mountAndExtract(isoPath string, entries []ISOEntry) error {
	if _, err := os.Stat(isoPath); err != nil {
		return fmt.Errorf("iso source %s: %w", isoPath, err)
	}

	mountPoint, err := os.MkdirTemp("", "metal-bootstrap-iso-")
	if err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}
	defer os.Remove(mountPoint)

	if err := mountISO(isoPath, mountPoint); err != nil {
		return err
	}
	defer func() {
		if err := unmountISO(mountPoint); err != nil {
			fmt.Fprintf(os.Stderr, "warn: unmount %s: %v\n", mountPoint, err)
		}
	}()

	for _, e := range entries {
		if err := copyOut(filepath.Join(mountPoint, e.Src), e.Dest); err != nil {
			return err
		}
	}

	return nil
}

// mountISO mounts `isoPath` read-only at `mountPoint` by invoking the
// system `mount` command with `-o loop,ro`. The `mount` command handles
// loop device allocation and association; mount(2) on a regular file
// fails with "block device required" for iso9660, which is why an
// external command is used here.
//
// Requires:
//   - `mount` from util-linux on PATH
//   - CAP_SYS_ADMIN (typically: run as root)
//   - a kernel with iso9660 and loop device support
func mountISO(isoPath, mountPoint string) error {
	cmd := exec.Command("mount", "-o", "loop,ro", "-t", "iso9660", isoPath, mountPoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount %s on %s: %w: %s", isoPath, mountPoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unmountISO unmounts `mountPoint` by invoking the system `umount` command.
// This releases the loop device that `mount -o loop` allocated.
func unmountISO(mountPoint string) error {
	cmd := exec.Command("umount", mountPoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s: %w: %s", mountPoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyOut copies `src` (a path inside the mounted ISO) to `dest` atomically.
func copyOut(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d", dest, os.Getpid())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, tmp, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
	}

	return nil
}
