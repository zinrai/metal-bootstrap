package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// processISO decides what to do with the iso: section of one target and
// either performs the work or, when dryRun is true, only prints the
// intended actions.
//
// Entries are grouped by `From` so that each ISO is opened at most once
// per target. If every entry in a group can be skipped, the ISO is not
// opened at all.
//
// Skip decision for one entry:
//   - Dest does not exist -> extract
//   - Dest exists         -> present
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
	// Print decisions and collect the work that still needs reading from
	// the ISO.
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

	return extractFromISO(isoPath, pending)
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

// extractFromISO reads each pending entry directly out of the ISO9660
// image at isoPath and writes it to its Dest, without mounting anything.
//
// The image is read through an io.ReaderAt, so only the volume descriptor,
// the directory records along each Src path, and the target file's own
// extent are touched. The bulk of the image (for Ubuntu, a multi-GB
// squashfs) is never read and never held in memory.
func extractFromISO(isoPath string, entries []ISOEntry) error {
	f, err := os.Open(isoPath)
	if err != nil {
		return fmt.Errorf("open iso %s: %w", isoPath, err)
	}
	defer f.Close()

	root, err := isoRoot(f)
	if err != nil {
		return fmt.Errorf("iso %s: %w", isoPath, err)
	}

	for _, e := range entries {
		node, err := isoLookup(f, root, e.Src)
		if err != nil {
			return fmt.Errorf("iso %s: %s: %w", isoPath, e.Src, err)
		}
		if node.isDir {
			return fmt.Errorf("iso %s: %s is a directory, not a file", isoPath, e.Src)
		}
		section := io.NewSectionReader(f, int64(node.lba)*isoSectorSize, int64(node.size))
		if err := writeOut(e.Dest, section); err != nil {
			return err
		}
	}
	return nil
}

// --- Minimal ISO9660 reader ------------------------------------------
//
// Just enough of ECMA-119 to locate a file by path and read its extent.
// Only the Primary Volume Descriptor and its directory tree are consulted:
// Joliet and Rock Ridge extensions are ignored because the files this tool
// extracts (a distro's casper/vmlinuz and casper/initrd) are reachable by
// their plain, short ISO9660 names. Multi-extent files are not handled;
// the extract targets are small single-extent kernel and initrd files.

const (
	isoSectorSize    = 2048
	isoFirstDescr    = 16   // the volume descriptor set starts here
	isoDirFlagSubdir = 0x02 // "directory" bit in a record's file flags
)

// isoNode is one located directory entry: where its data begins (in
// sectors) and how many bytes it holds.
type isoNode struct {
	lba   uint32
	size  uint32
	isDir bool
}

// isoRoot scans the volume descriptor set for the Primary Volume
// Descriptor and returns its root directory record.
func isoRoot(r io.ReaderAt) (isoNode, error) {
	sector := make([]byte, isoSectorSize)
	for s := isoFirstDescr; ; s++ {
		if _, err := r.ReadAt(sector, int64(s)*isoSectorSize); err != nil {
			return isoNode{}, fmt.Errorf("read volume descriptor at sector %d: %w", s, err)
		}
		// Every volume descriptor carries the standard identifier
		// "CD001" in bytes 1..6; its absence means this is not ISO9660.
		if string(sector[1:6]) != "CD001" {
			return isoNode{}, fmt.Errorf("not an iso9660 image (no CD001 at sector %d)", s)
		}
		switch sector[0] { // descriptor type
		case 1: // primary: root directory record is 34 bytes at offset 156
			return parseDirRecord(sector[156 : 156+34])
		case 255: // volume descriptor set terminator
			return isoNode{}, fmt.Errorf("no primary volume descriptor")
		}
		// Boot record (0), supplementary (2), etc.: keep scanning.
	}
}

// isoLookup walks the "/"-separated srcPath from root and returns the
// node it names.
func isoLookup(r io.ReaderAt, root isoNode, srcPath string) (isoNode, error) {
	node := root
	for _, part := range strings.Split(srcPath, "/") {
		if part == "" {
			continue
		}
		if !node.isDir {
			return isoNode{}, fmt.Errorf("%s: parent is not a directory", part)
		}
		child, found, err := isoChild(r, node, part)
		if err != nil {
			return isoNode{}, err
		}
		if !found {
			return isoNode{}, fmt.Errorf("%s: not found in iso", part)
		}
		node = child
	}
	return node, nil
}

// isoChild scans directory `dir` for a child named `name`, matched
// case-insensitively against the plain ISO9660 identifier with its
// ";version" suffix and any trailing "." removed.
func isoChild(r io.ReaderAt, dir isoNode, name string) (isoNode, bool, error) {
	buf := make([]byte, dir.size)
	if _, err := r.ReadAt(buf, int64(dir.lba)*isoSectorSize); err != nil {
		return isoNode{}, false, fmt.Errorf("read directory extent: %w", err)
	}

	want := strings.ToUpper(name)
	pos := 0
	for pos < len(buf) {
		recLen := int(buf[pos])
		if recLen == 0 {
			// No further records in this logical sector; a directory
			// extent is a sequence of sectors, so jump to the next one.
			pos = (pos/isoSectorSize + 1) * isoSectorSize
			continue
		}
		if pos+recLen > len(buf) {
			break
		}
		rec := buf[pos : pos+recLen]
		pos += recLen

		id := recordName(rec)
		if id == "" {
			continue // the "." and ".." entries
		}
		if strings.ToUpper(id) == want {
			node, err := parseDirRecord(rec)
			if err != nil {
				return isoNode{}, false, err
			}
			return node, true, nil
		}
	}
	return isoNode{}, false, nil
}

// parseDirRecord reads the extent location, data length, and directory
// flag out of one directory record. Numeric fields are stored both-endian;
// the little-endian halves are read here.
func parseDirRecord(rec []byte) (isoNode, error) {
	if len(rec) < 33 {
		return isoNode{}, fmt.Errorf("short directory record (%d bytes)", len(rec))
	}
	return isoNode{
		lba:   binary.LittleEndian.Uint32(rec[2:6]),
		size:  binary.LittleEndian.Uint32(rec[10:14]),
		isDir: rec[25]&isoDirFlagSubdir != 0,
	}, nil
}

// recordName returns the usable file identifier of a directory record, or
// "" for the "." and ".." self/parent entries.
func recordName(rec []byte) string {
	idLen := int(rec[32])
	if idLen == 0 || 33+idLen > len(rec) {
		return ""
	}
	id := rec[33 : 33+idLen]
	if idLen == 1 && (id[0] == 0x00 || id[0] == 0x01) {
		return "" // 0x00 = ".", 0x01 = ".."
	}
	name := string(id)
	if i := strings.IndexByte(name, ';'); i >= 0 {
		name = name[:i] // drop the ";version" suffix
	}
	return strings.TrimSuffix(name, ".")
}

// writeOut writes r to dest atomically: into a sibling tmp file that is
// renamed onto dest after the copy completes.
func writeOut(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", dest, err)
	}

	tmp := fmt.Sprintf("%s.tmp.%d", dest, os.Getpid())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}

	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("copy to %s: %w", tmp, err)
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
