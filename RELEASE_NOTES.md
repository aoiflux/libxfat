# libxfat v1.4.1

Released 2026-10-29.

```sh
go get github.com/aoiflux/libxfat@v1.4.1
```

A walk can now tell you what it could not reach. Until this release, a directory
the walk skipped, or read only in part, looked exactly like an empty or complete
one, and a free-space sweep that missed part of the volume returned nil like a
full one. For forensic work, an index has to say what it did not cover. Two
optional callbacks now report every such case, and a bug that lost live
subtrees from walks that include deleted records is fixed.

## Compatibility

- **Additive API.** Nothing was removed, renamed or re-signatured. New:
  `WalkOptions.OnSkip`, `WalkOptions.OnRecoveredScan`, `SkipReason` with six
  constants, and `RecoveredScanResult`.
- **Same output without the callbacks**, with one exception: a walk with
  `IncludeDeleted` now reports live subtrees it used to drop (see Fixed).
  `ReportDeep` gains the same rows. `Walk`, the default `WalkWithOptions` and
  `Report` are unchanged.
- **Report schema unchanged.** `schema_version` stays `1`, and `library_version`
  reads `v1.4.1`.

## New: `WalkOptions.OnSkip`

It is called for every directory the walk reported but did not descend into, or
could read only in part. Each call carries a reason, and for a failed or partial
read, the error:

| Reason | Meaning | `err` |
|---|---|---|
| `SkipReadError` | The contents could not be read. This includes a live directory whose name is only spaces, which `ReadDir` declines without an error. | the read error, or one wrapping `ErrInvalidEntry` |
| `SkipDepthLimit` | The directory sits on `MaxDepth`. | nil |
| `SkipRevisit` | Its first cluster was already walked: a cycle or a cross-link. | nil |
| `SkipInvalidCluster` | Its first cluster is outside the cluster heap. | nil |
| `SkipDeletedUnrecoverable` | `DescendDeletedDirectories` is set, but the deleted directory's first cluster is in use again. | nil |
| `SkipPartialRead` | It was descended, but only part could be read: the image ends first, or a deleted directory's range reaches a reused cluster. The children that were read still follow. | the EOF, or one wrapping `ErrDeletedEntry` |

```go
err := fs.WalkWithOptions(ctx, libxfat.WalkOptions{
	IncludeDeleted:            true,
	DescendDeletedDirectories: true,
	OnSkip: func(path string, e libxfat.Entry, reason libxfat.SkipReason, err error) error {
		log.Printf("not fully walked: %s (%s): %v", path, reason, err)
		return nil
	},
}, fn)
```

Rules worth knowing:

- `OnSkip` is called only for the entry `fn` was handed immediately before, with
  the same path, and at most once per entry.
- Nothing is reported for files, metadata or synthetic entries, records that
  `IncludeDeleted` left out, or deleted directories when
  `DescendDeletedDirectories` is off.
- With `StopOnReadError`, an unreadable directory still aborts the walk, and
  `OnSkip` is not called for it. A blank-named directory and a partial read
  never aborted a walk, and still do not; they are reported either way.
- An error returned from `OnSkip` stops the walk and is returned unchanged.
- The first five reasons have the same names and values as in libfat.
  `SkipPartialRead` exists only here, because a FAT directory is read whole or
  fails.

## New: `WalkOptions.OnRecoveredScan`

With `IncludeRecovered`, it is called once with a `*RecoveredScanResult`, before
any carved entry is reported:

- `ClustersSwept`: the unallocated clusters read.
- `ClustersUnclassified`: heap clusters the allocation bitmap could not answer
  for, because the image ends inside the `$BitMap` stream.
- `Truncated`: set when `ClustersUnclassified` is not zero.

An error returned from it stops the walk, and no carved entry is reported. Its
shape follows libfat's `OnOrphanScan`.

## Fixed

**A walk including deleted records could lose a live directory's whole
subtree.** A deleted directory claimed its first cluster in the walk's cycle
guard, even when the walk never went into it. A live directory created later in
the same cluster was then skipped as a revisit. That is the ordinary result of
removing one directory and creating another. A deleted directory now claims its
cluster only once it is descended into, so loops of deleted records are still
stopped.

## More

See [CHANGELOG.md](CHANGELOG.md) for the complete entry and earlier releases.
