# Durability and process-ownership contract

This document states what byodb commits guarantee and, equally importantly,
what they assume. The implementation is an educational single-writer database,
not a substitute for PostgreSQL, SQLite, replication consensus, or a managed
backup service.

## Commit protocol

A writing transaction builds private changes in memory and then:

1. checks optimistic read/write conflicts against newer commits;
2. applies its sorted changes to the latest copy-on-write B+tree root;
3. assigns physical pages without reusing pages visible to an older snapshot;
4. writes new data pages and the versioned free-list pages;
5. calls `Sync` on the database file;
6. writes the alternate checksummed metadata page with the new root/version;
7. calls `Sync` again; and
8. updates the process's in-memory root.

The second successful sync is the durable commit point. Before metadata is
written, only the old root is visible. Between metadata write and metadata sync,
recovery may observe the old or the complete new root. After the sync returns,
recovery is expected to observe the new root under the filesystem assumptions
below.

An I/O error before the commit point returns an error. An error or process loss
after the metadata sync is an **ambiguous acknowledgement**: the data may be
durable even if the caller did not receive success. Applications that perform
external side effects need idempotency keys; the database cannot make a remote
API call and a local file commit exactly once together.

## Metadata and corruption detection

- Two 4 KiB metadata pages alternate by transaction version.
- Each metadata and free-list page has a CRC32 checksum.
- Opening selects the highest-version valid metadata/free-list pair and can
  fall back one committed root when the newest metadata is invalid.
- Ordinary B+tree data pages do **not** currently have per-page checksums.
  Undetected storage/media corruption in a data page may surface as an invalid
  pointer, decode error, or panic rather than clean fallback.
- The database does not repair media corruption, scrub pages, or provide
  redundant copies of application data. Keep external backups.

## Filesystem assumptions

The durability claim assumes:

- a local filesystem and kernel where `os.File.Sync` persists file contents and
  required metadata according to the platform contract;
- stable storage that honors completed flushes;
- atomic aligned visibility of the checked metadata page or checksum failure;
- no hostile program modifying the file behind the database; and
- adequate disk space for copy-on-write pages and a later metadata page.

Network filesystems, synchronized cloud folders, removable media, broken disk
write caches, and copying a live file with an unrelated file utility are not
validated configurations.

When a new database or backup name is created, Unix-like builds sync its parent
directory after file creation/rename. Go on Windows cannot sync an open
directory through the standard library, so the Windows build syncs the database
file but cannot make the same directory-entry persistence claim. This is the
reason for the separate `sync_parent_windows.go` implementation.

## Process locks

- A read/write open requests a non-blocking exclusive lock on the database file.
- A read-only open requests a non-blocking shared lock.
- Multiple read-only byodb processes may coexist; a writer excludes every other
  byodb opener of that exact file.
- Lock conflicts return `ErrDatabaseLocked` immediately instead of waiting.
- Unix `flock` is advisory: safety requires every participant to honor it.
- Windows uses `LockFileEx` over the first byte and releases it on close/process
  termination.

The lock prevents two engines from publishing unrelated metadata roots. It does
not implement live replica refresh. A reader that needs to operate while the
writer continues should open a separate snapshot file.

Locks are not claimed to coordinate the same path across NFS/SMB hosts,
container nodes, or copied aliases with different filesystem identity.

## Snapshot backups and read-only replicas

`DB.Backup(target)` holds the pager commit mutex, copies exactly the current
durable page count to a temporary file, syncs it, closes it, renames it to the
target, and syncs the parent directory where supported. Transactions may keep
building private memory state, but commits wait while the snapshot is copied.

- The target must not already exist and must differ from the source path.
- The temporary file and target must be on the same filesystem for rename
  atomicity.
- A crash before rename may leave an internal temporary file; it does not make
  a partial target visible.
- A snapshot is immutable point-in-time data by convention. Opening it with
  `OpenDBReadOnly` enforces that convention at commit.
- There is no incremental backup, log shipping, live catch-up, retention
  policy, encryption, or remote upload.

## Snapshots, conflicts, and free-page reuse

Transactions read an immutable root version. Pending writes are merged over
that snapshot. At commit, dependencies are compared with newer committed write
ranges; overlap returns `ErrConflict`, and the caller must restart its complete
transaction from `Begin`.

Pages become reusable only after every transaction that could still reach them
has ended. This protects in-process snapshot readers. The process lock is what
prevents another writer—with a different in-memory oldest-reader set—from
reusing those pages.

## Format compatibility

Storage format 3 adds leaf type 3, which stores a common key prefix once and
per-entry suffixes. Format-3 code accepts format-2 metadata (reserved format
field zero) and the original uncompressed node types. The first writing commit
publishes format 3. Read-only opening does not rewrite an old file.

This repository does not promise that an older binary can safely open a file
after a format-3 commit. Keep the executable/commit identifier with important
snapshots and test downgrade/upgrade behavior before relying on it.

## Explicit non-goals

The current guarantees exclude multi-writer operation, distributed consensus,
live replication, automatic failover, cross-database transactions, user/role
authorization, encryption at rest, secure deletion, online compaction, and
proof against arbitrary hardware corruption. The HTTP service is a local
portfolio/research system and should not be represented as an internet-facing
trading platform.
