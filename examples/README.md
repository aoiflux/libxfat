# Examples

This folder contains small runnable programs that show the public libxfat API in
common inspection and extraction workflows.

## Run An Example

```bash
go run ./examples/list-root -image /path/to/volume.exfat
go run ./examples/list-all -image /path/to/volume.exfat
go run ./examples/volume-stats -image /path/to/volume.exfat
go run ./examples/extract-all -image /path/to/volume.exfat -out ./recovered
```

Common flags:

- `-image`: path to the exFAT image file.
- `-optimistic`: skip strict VBR offset verification.
- `-offset`: sector offset where the exFAT volume begins.

The `extract-all` example also requires:

- `-out`: output directory where recovered files will be written.

The `list-all` example also accepts:

- `-deleted`: report deleted and recovered entries alongside the live tree.

## Included Programs

- `list-root`: open an image and print root directory entries, including
  metadata and virtual entries.
- `list-all`: walk the volume with `WalkWithOptions` and print one line per
  entry - kind, marks, size, `FileID` and full path. Pass `-deleted` to include
  deleted records, descend deleted directories, and report entry sets carved out
  of free space.
- `volume-stats`: parse the root directory and report volume label, cluster
  size, used space, allocation counts, and metadata entry totals.
- `extract-all`: extract all regular files reachable from the root directory
  into an output directory while preserving directory structure.
