# libxfat v1.4.2

Released 2026-11-09.

```sh
go get github.com/aoiflux/libxfat@v1.4.2
```

A fix release. Files and directories with long names, 241 to 255 code units,
were silently missing from every listing. They now appear.

## Compatibility

- **No API change.** Nothing was added, removed, renamed or re-signatured.
- **Report schema unchanged.** `schema_version` stays `1`, and `library_version`
  reads `v1.4.2`.
- **More entries, never fewer.** The only difference in output is the entries
  with such names, which were missing before.

## Fixed

**Names of 241 to 255 code units were lost.** Each name record holds 15 code
units, so such a name needs 17 of them. With the stream extension, its file
entry set then has a SecondaryCount of 18, the most the exFAT specification
allows. The file entry check refused anything over 17, so the entry was left
out of `ReadDir`, `Walk`, `RecoverDeletedEntries` and the report. That covered
live and deleted entries alike, with no error. Sets of up to 18 secondaries are
now accepted. Thanks to donran for the fix (#1).

## More

See [CHANGELOG.md](CHANGELOG.md) for the complete entry and earlier releases.
v1.4.1 added `WalkOptions.OnSkip` and `WalkOptions.OnRecoveredScan`. Its notes
are in the changelog.
