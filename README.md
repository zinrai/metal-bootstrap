# metal-bootstrap

Fetch files from URLs and extract files from ISO images according to a YAML
configuration. Idempotent: running it repeatedly produces the same on-disk
state.

## What it does

`metal-bootstrap` performs two kinds of operations described in a single
YAML configuration file:

1. Download a file from an HTTP(S) URL and place it at a chosen path,
   verifying its SHA-256.
2. Mount a local ISO image read-only and copy selected files out of it
   to chosen paths.

Both operations are idempotent. A second run skips work whose output is
already in place.

## Use case

A typical use is preparing the kernel, initrd, and other artifacts that a
PXE/iPXE-booted installer fetches over HTTP. Different operating systems
publish these in different forms:

- AlmaLinux, Debian: kernel and initrd are downloadable as individual files.
- Ubuntu Server (autoinstall): kernel and initrd live inside the ISO image
  and must be extracted.

`metal-bootstrap` covers both shapes in one configuration.

## Requirements

- `mount` and `umount` from util-linux on PATH (for `iso:` operations).
- Linux kernel with `iso9660` and loop device support.
- `CAP_SYS_ADMIN` (typically: run as root) when `iso:` operations are
  present. The mount call requires it. Configurations with only `files:`
  do not need elevated privileges beyond what is required to write the
  `dest` paths.

## Usage

```
metal-bootstrap -config path/to/config.yaml
metal-bootstrap -config path/to/config.yaml -dry-run
```

Flags:

- `-config`: path to YAML config (default: `config.yaml`)
- `-dry-run`: print actions without making changes

Exit status is 0 on success and non-zero on the first error.

## Configuration

A target is a named group of operations. A target may have any combination
of `files:` (HTTP fetches) and `iso:` (ISO extractions).

See `config.yaml.example` for a fuller example.

### files

Each entry under `files:` describes one HTTP fetch:

- `url`: source URL
- `dest`: absolute path where the file will be placed
- `sha256`: expected SHA-256 of the file content, lowercase hex

### iso

Each entry under `iso:` describes one extraction from a local ISO image:

- `from`: absolute path to the ISO file on disk
- `src`: path inside the ISO (relative, no leading slash)
- `dest`: absolute path where the extracted file will be placed

`from` is expected to be a file that was placed earlier in the same run
(typically by a `files:` entry in the same target) or that already exists
on disk. The same ISO is mounted only once per run, even when multiple
`iso:` entries reference it.

## Idempotency

A `files:` entry is skipped when `dest` exists and its SHA-256 matches the
declared `sha256`.

An `iso:` entry is skipped when `dest` exists. SHA-256 of the source ISO
is the responsibility of the `files:` entry that declares it; `iso:` does
not re-verify the source.

If every entry in a target's `iso:` is skippable, the ISO is not mounted
at all.

## Dry run

Pass `-dry-run` to print the actions that would be taken without making
any changes. Each line of output describes one decision:

- `present: <path> (sha256 matches)` -- `files:` dest exists, SHA-256 matches
- `present: <path>` -- `iso:` dest exists
- `fetch:   <dest> <- <url>` -- HTTP download will run
- `extract: <dest> <- <iso>!<src>` -- ISO extraction will run

`-dry-run` reads files from disk (it computes SHA-256 of existing `files:`
entries to make the decision) but never writes, downloads, or mounts.

## Safety against interruption

Downloads and extractions write to a sibling temporary file and are renamed
onto `dest` only on success. A crash or `SIGKILL` mid-run leaves at most a
stale `dest.tmp.<pid>` next to `dest`; `dest` itself stays either absent or
in its previous good state.

A failed SHA-256 check after download deletes the temporary file and exits
with a non-zero status. `dest` is not modified.

## Non-goals

- Repository mirroring (use `rsync`, `apt-mirror`, `reposync`, or similar).
- DHCP, TFTP, or HTTP server setup.
- Building iPXE binaries.
- Generating per-node boot configuration.
- Any awareness of how the placed files will be used downstream. The
  `dest` paths are whatever the operator chooses.

## License

This project is licensed under the [MIT License](./LICENSE).
