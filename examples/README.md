# Examples

This folder contains small runnable programs that show the public libxfat API in
common workflows.

## Run An Example

```bash
go run ./examples/list-root -image /path/to/volume.exfat
go run ./examples/volume-stats -image /path/to/volume.exfat
go run ./examples/extract-all -image /path/to/volume.exfat -out ./recovered
```

Each program also accepts:

- `-optimistic` to skip strict VBR offset verification.
- `-offset` to point at an exFAT volume stored at a sector offset inside a
  larger image.

## Included Programs

- `list-root`: open an image and print the root directory entries, including
  metadata and virtual entries.
- `volume-stats`: parse the root directory and report cluster counts, cluster
  size, used-space percentage, and root entry counts.
- `extract-all`: extract every regular file reachable from the root directory
  into an output directory.
