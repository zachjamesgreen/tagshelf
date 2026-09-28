# SQLite driver for tagshelf: modernc vs mattn vs ncruces

Researched: 2026-09-27

Question: Which SQLite driver should tagshelf use? It should build on Windows without a C compiler, support WAL with the CLI and TUI open at once, handle a library of tens of thousands of files, and keep the binary small. The module is on `go 1.21`, and the driver should be well maintained.

Method: I used primary sources only. These were each module's `go.mod`, README and source (fetched from the Go module proxy at the versions below), `proxy.golang.org` version and time metadata, the GitHub and GitLab APIs, and sqlite.org. I also ran my own measurements on this machine (Windows 11 amd64, Go 1.27.1, no C compiler on `PATH`, `go env CGO_ENABLED` = `0`). The harness is described in [Appendix: how the measurements were taken](#appendix-how-the-measurements-were-taken).

Versions compared (latest on the proxy, 2026-09-27):

| Driver | Version | Released | Bundled SQLite |
|---|---|---|---|
| `modernc.org/sqlite` | v1.59.0 | 2026-09-15 ([proxy](https://proxy.golang.org/modernc.org/sqlite/@latest)) | 3.53.4 ([doc.go](https://gitlab.com/cznic/sqlite/-/blob/v1.59.0/doc.go), confirmed with `select sqlite_version()`) |
| `github.com/mattn/go-sqlite3` | v1.14.52 | 2026-09-05 ([proxy](https://proxy.golang.org/github.com/mattn/go-sqlite3/@latest)) | 3.53.4 (`SQLITE_VERSION` in [sqlite3-binding.c](https://github.com/mattn/go-sqlite3/blob/v1.14.52/sqlite3-binding.c)) |
| `github.com/ncruces/go-sqlite3` | v0.35.6 | 2026-09-23 ([proxy](https://proxy.golang.org/github.com/ncruces/go-sqlite3/@latest)) | 3.53.4 (confirmed with `select sqlite_version()`) |

## Summary

**Recommendation: use `modernc.org/sqlite`, and raise tagshelf's `go` directive from 1.21 to 1.25.**

- **Only the two pure-Go drivers build with no C compiler.** `mattn/go-sqlite3` is cgo-only. With `CGO_ENABLED=0` it still compiles, but only as a stub that fails at runtime.
- **Both pure-Go drivers handled WAL with several processes at once.** In my test, two writer processes and one reader process shared one database for 10 seconds with zero errors and `integrity_check = ok`. That held for each pure-Go driver on its own and for modernc and ncruces processes mixed on the same file. modernc runs SQLite's own Windows VFS (`os_win.c`, transpiled to Go). ncruces runs its own Go rewrite of that VFS, which its README says is "not as battle tested as the original".
- **At tagshelf's scale, their speed is the same.** At 50,000 rows, both pure-Go drivers loaded the index in about 0.45 s, did a full scan in 40-55 ms, found duplicate groups in about 12 ms, and did 5,000 point lookups in about 36 ms.
- **modernc makes a smaller binary**: 6.5 MB stripped, against 8.4 MB for ncruces.
- **modernc needs an older Go**: 1.25, against 1.26 for ncruces.
- **Both pure-Go drivers need a newer Go than 1.21.** The last releases that allow 1.21 are about 18 months old. This machine already runs Go 1.27.1, so the bump costs nothing locally.

## Comparison table

| | `modernc.org/sqlite` | `mattn/go-sqlite3` | `ncruces/go-sqlite3` |
|---|---|---|---|
| How it works | SQLite C transpiled to Go | cgo binding to the SQLite C amalgamation | SQLite compiled to Wasm, then translated to Go with wasm2go |
| Builds on Windows without a C compiler | **Yes** | **No** (needs gcc and `CGO_ENABLED=1`) | **Yes** |
| Windows file locking / WAL shared memory | SQLite's own `os_win.c`, transpiled | SQLite's own `os_win.c` | Go reimplementation using `LockFileEx` and `MapViewOfFile` |
| Test with several processes (measured) | 0 errors, integrity ok | not testable here (no gcc) | 0 errors, integrity ok |
| 50k rows: load / scan / lookups (measured) | ~0.46 s / ~52 ms / ~37 ms | not testable here | ~0.44 s / ~38 ms / ~37 ms |
| Stripped binary size (`-s -w`, measured) | 6.5 MB | 2.3 MB (stub only, see below) | 8.4 MB |
| `go` directive of latest release | **1.25.0** | **1.21** | **1.26.0** |
| Last release that allows go 1.21 | v1.36.1 (2025-03-12) | current | v0.22.0 (2025-01-14) |
| Releases in the last 12 months | 34 | 20 | 26 |
| Maintainers | Jan Mercl is the main author (514 commits) | Mainly mattn (713 commits); 148 open issues and PRs | Mainly ncruces (1,079 commits); 5 open issues and PRs |

Sources and details follow in the sections below.

## Building on Windows without a C compiler

- **mattn:** The README says "_go-sqlite3_ is *cgo* package. If you want to build your app using go-sqlite3, you need gcc", and "you are required to set the environment variable `CGO_ENABLED=1` and have a `gcc` compiler present within your path" ([README](https://github.com/mattn/go-sqlite3/blob/v1.14.52/README.md#installation)). For Windows it tells you to install a gcc toolchain such as TDM-GCC ([README, Windows](https://github.com/mattn/go-sqlite3/blob/v1.14.52/README.md#windows)). With cgo off, the build picks up `static_mock.go` (`//go:build !cgo`). That file makes every open fail with `"Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub"` ([static_mock.go](https://github.com/mattn/go-sqlite3/blob/v1.14.52/static_mock.go)). I checked this: the build succeeded, and the first query panicked with exactly that message. This machine has no `gcc`, `cc`, `clang` or `zig`, and `go env CGO_ENABLED` prints `0`.
- **modernc:** The package is documented as "a sql/database driver using a CGo-free port of the C SQLite3 library". It lists `windows/386`, `windows/amd64` and `windows/arm64` as supported ([doc.go](https://gitlab.com/cznic/sqlite/-/blob/v1.59.0/doc.go)). I checked this: with `CGO_ENABLED=0` it built and worked.
- **ncruces:** The README calls it "a `cgo`-free SQLite wrapper". It "wraps a Wasm build of SQLite, and uses wasm2go to translate it to Go", and "Go and `x/sys` are the _only_ required dependencies". Every commit is tested on Windows amd64 and arm64 ([README](https://github.com/ncruces/go-sqlite3/blob/v0.35.6/README.md)). I checked this: with `CGO_ENABLED=0` it built and worked. Note that current versions no longer run through a Wasm runtime at run time. The Wasm is translated to Go ahead of time, and the module depends on `github.com/ncruces/go-sqlite3-wasm/v6` ([go.mod](https://proxy.golang.org/github.com/ncruces/go-sqlite3/@v/v0.35.6.mod)).

## WAL and access from two processes at once

What SQLite itself guarantees in WAL mode ([sqlite.org/wal.html](https://sqlite.org/wal.html)):

- Readers do not block writers, and writers do not block readers.
- There can be only one writer at a time.
- "All processes using a database must be on the same host computer; WAL does not work over a network filesystem." For tagshelf, the database must stay on a local disk, even when the music library lives on a NAS.

Each driver's Windows implementation:

- **mattn:** It is the C SQLite with its standard Windows VFS. It supports `_journal_mode=WAL`, `_busy_timeout` and `_txlock=immediate` as DSN options ([README, connection string](https://github.com/mattn/go-sqlite3/blob/v1.14.52/README.md#connection-string)).
- **modernc:** It runs SQLite's own VFS code, transpiled. The Windows lock and WAL shared-memory routines exist as Go translations of the C functions (`_winLock`, `_winShmMap` in `lib/sqlite_windows.go`). Locking therefore behaves exactly like C SQLite on Windows. The DSN accepts `_pragma=...` (for example `journal_mode(WAL)` and `busy_timeout(5000)`) and `_txlock` ([sqlite.go](https://gitlab.com/cznic/sqlite/-/blob/v1.59.0/sqlite.go)).
- **ncruces:** It replaces SQLite's OS layer with "a pure Go implementation". The README says: "Since this is a from scratch reimplementation, there are naturally some ways it deviates from the original. It's also not as battle tested as the original." On Windows it uses `LockFileEx`/`UnlockFileEx` and `MapViewOfFile` "like SQLite". Its default build "is compatible with the standard Unix and Windows SQLite VFSes". It is tested with SQLite's `mptest` multi-process test ([vfs/README.md](https://github.com/ncruces/go-sqlite3/blob/v0.35.6/vfs/README.md), [README](https://github.com/ncruces/go-sqlite3/blob/v0.35.6/README.md#testing)). The README also warns that each connection runs in a sandbox, so "memory usage will be higher than alternatives".

What I measured, on a 50,000-row `files` table in WAL mode with `busy_timeout=5000`, `synchronous=NORMAL` and `_txlock=immediate`. Three processes ran for 10 seconds: two writers doing small autocommit `UPDATE`s on different rows, and one reader running indexed `count(*)` queries.

| Setup | Writer 1 ok / fail | Writer 2 ok / fail | Reader ok / fail | `integrity_check` after |
|---|---|---|---|---|
| modernc ×3 | 126,874 / 0 | 128,912 / 0 | 375,661 / 0 | ok |
| ncruces ×3 | 121,071 / 0 | 120,732 / 0 | 362,124 / 0 | ok |
| modernc writer + ncruces writer + ncruces reader | 177,784 / 0 | 160,931 / 0 | 297,884 / 0 | ok (checked with both drivers) |

With a busy timeout set, both pure-Go drivers handled heavy contention from several processes on Windows without `SQLITE_BUSY` errors. The mixed run also confirms ncruces's claim that it can share a database with the standard Windows VFS. That matters if another SQLite tool, such as the `sqlite3` shell or DB Browser, opens the database while tagshelf runs.

Whatever the driver, tagshelf should set `journal_mode=WAL`, a `busy_timeout`, and `BEGIN IMMEDIATE` for write transactions (`_txlock=immediate`). SQLite's docs say that when a read transaction tries to upgrade to a write "and some other database connection has already modified the database or is already in the process of modifying the database", then "the write statement will fail with SQLITE_BUSY" ([sqlite.org/lang_transaction.html](https://sqlite.org/lang_transaction.html)).

## Performance for tens of thousands of files

Measured on this machine, three runs each, 50,000 rows. The table has a text primary key on `path` and indexes on `hash` and on `(artist, album)`. It holds 2,500 duplicate hash groups.

| Operation | modernc (3 runs) | ncruces (3 runs) |
|---|---|---|
| Insert 50k rows in one transaction | 453 / 494 / 455 ms | 442 / 445 / 436 ms |
| Full scan into Go (50k rows, 8 columns) | 53 / 50 / 53 ms | 41 / 36 / 38 ms |
| Duplicate groups (`GROUP BY hash HAVING count(*)>1`) | 12 / 12 / 12 ms | 14 / 14 / 14 ms |
| 5,000 point lookups by path | 38 / 36 / 36 ms | 36 / 42 / 33 ms |
| `UPDATE` all 50k rows | 206 / 222 / 188 ms | 174 / 259 / 171 ms |

- **The two pure-Go drivers are within noise of each other.** At this size every operation tagshelf needs finishes in well under a second. The time to scan a music library (reading tags from tens of thousands of files) will far outweigh any database time.
- **mattn** could not be measured here, because there is no C compiler. The modernc docs give the gap to native C. On CPU-bound queries, modernc takes 2.0×, 1.9× and 1.3× the CPU time of the same SQLite built from C, on linux/amd64. I/O-bound work "is dominated by the operating system either way" ([doc.go, Performance](https://gitlab.com/cznic/sqlite/-/blob/v1.59.0/doc.go)). So mattn might roughly halve some already-millisecond query times. That does not justify requiring gcc.
- **Pool size (modernc):** The modernc docs advise bounding the `database/sql` pool with `SetMaxOpenConns`, because each connection has its own page cache and libc thread state (same doc section).

## Binary size

The test program imports `database/sql` and one driver. Sizes were measured on windows/amd64 with Go 1.27.1 and `CGO_ENABLED=0`.

| Driver | Default build | `-ldflags "-s -w"` |
|---|---|---|
| modernc | 9.96 MB | 6.48 MB |
| ncruces | 14.16 MB | 8.39 MB |
| mattn (cgo stub only, no SQLite inside) | 3.40 MB | 2.30 MB |

The mattn row is a baseline: the stub contains no SQLite. The pure-Go drivers add roughly 4 MB (modernc) and 6 MB (ncruces) to a stripped binary. A real cgo mattn build could not be produced here. It would be smaller than either, because compiled C SQLite is around 1 MB, but it would need gcc.

## Go version

- **Latest `go` directives**, from the proxy `.mod` files:
  - modernc v1.59.0: `go 1.25.0` ([mod](https://proxy.golang.org/modernc.org/sqlite/@v/v1.59.0.mod))
  - mattn v1.14.52: `go 1.21` ([mod](https://proxy.golang.org/github.com/mattn/go-sqlite3/@v/v1.14.52.mod))
  - ncruces v0.35.6: `go 1.26.0` ([mod](https://proxy.golang.org/github.com/ncruces/go-sqlite3/@v/v0.35.6.mod))
- **Effect on tagshelf:** I ran `go get` for each driver in a fresh module declaring `go 1.21`. For modernc, Go printed `go: upgraded go 1.21 => 1.25.0`. For ncruces it printed `go: upgraded go 1.21 => 1.26.0`. For mattn the module stayed on `go 1.21`.
- **Last versions that allow go 1.21** (from each version's `.mod` and `.info` on the proxy):
  - modernc: v1.36.1, released 2025-03-12. v1.36.2 moved to `go 1.23.0`.
  - ncruces: v0.22.0, released 2025-01-14. v0.23.0 moved to `go 1.22`.
- **Staying on 1.21 is not a good trade.** It would mean pinning a driver 18 months or more behind, with an older SQLite and without later fixes. Go 1.21 itself has been out of support for a long time: Go supports each major release "until there are two newer major releases" ([go.dev release policy](https://go.dev/doc/devel/release#policy)). The local toolchain is already Go 1.27.1, so raising the directive to 1.25 has no practical cost.

## Maintenance

| | modernc | mattn | ncruces |
|---|---|---|---|
| Releases, 2025-09-27 to 2026-09-27 (proxy `.info` times) | 34 | 20 | 26 |
| Latest release | v1.59.0, 2026-09-15 | v1.14.52, 2026-09-05 | v0.35.6, 2026-09-23 |
| Latest repo activity | GitLab `cznic/sqlite` 2026-09-27 ([API](https://gitlab.com/api/v4/projects/cznic%2Fsqlite)) | GitHub push 2026-09-28 ([API](https://api.github.com/repos/mattn/go-sqlite3)) | GitHub push 2026-09-27 ([API](https://api.github.com/repos/ncruces/go-sqlite3)) |
| Top contributor (commits) | Jan Mercl 514; next is 37 | mattn 713; next is 97 | ncruces 1,079; next is dependabot, 72 |
| Open issues and PRs | 1 on the GitHub mirror (GitLab is the primary repo) | 148 | 5 |
| Version stability | v1, but several retracted releases (listed in [go.mod](https://proxy.golang.org/modernc.org/sqlite/@v/v1.59.0.mod)) | v1, stable API | v0: the API can still change between minor versions |

All three are actively maintained, and each rests mainly on one person.

modernc has one maintenance trap. Its docs, under "Fragile modernc.org/libc dependency", say you "should use in your go.mod file the exact same version of modernc.org/libc as seen in the go.mod file of this repository" ([doc.go](https://gitlab.com/cznic/sqlite/-/blob/v1.59.0/doc.go), [issue #177](https://gitlab.com/cznic/sqlite/-/issues/177)). When bumping modernc, bump `modernc.org/libc` in step, and don't let another dependency pull libc to a different version.

## Recommendation

**Use `modernc.org/sqlite`** (v1.59.0 now), and change tagshelf's `go.mod` from `go 1.21` to `go 1.25`.

Why:

1. **It builds with no C compiler.** That rules out mattn, which in a CGO-off build gives you a stub that fails at runtime.
2. **Its WAL and locking are the most trustworthy for a CLI and TUI running at once.** modernc runs SQLite's own `os_win.c` locking and shared-memory code, transpiled. ncruces uses a Go rewrite that its own README calls "not as battle tested as the original". Both passed my test with several processes, so this is about margin rather than a known defect.
3. **It is as fast as ncruces at tagshelf's scale.** Every operation on 50k rows takes tens of milliseconds, and the load takes under 0.5 s.
4. **It makes a smaller binary**: about 2 MB less than ncruces when stripped.
5. **It needs an older Go:** 1.25, against 1.26 for ncruces. It is mature (v1, years of releases) and actively maintained (34 releases in the last year).

Runner-up: `ncruces/go-sqlite3` is a sound alternative. It is slightly faster on scans in my runs, has a small open-issue count, and supports the standard Windows VFS. Choose it only if you want its extras (custom VFSes, encryption at rest, the direct C-style API) and can accept Go 1.26, a v0 API and higher memory use per connection.

What to do when adopting modernc:

- Pin `modernc.org/libc` to the exact version in modernc's `go.mod`.
- Open with `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate`.
- Bound the pool with `SetMaxOpenConns`.
- Keep the database file on a local disk.

## Appendix: how the measurements were taken

- **Harness:** A scratch module (outside the repo) with one `main.go`. Build tags choose the driver: `modernc.org/sqlite`, `github.com/ncruces/go-sqlite3/driver` or `github.com/mattn/go-sqlite3`. Everything goes through `database/sql` with `SetMaxOpenConns(4)`.
- **Schema:** `files(path TEXT PRIMARY KEY, size, mtime, hash TEXT, artist, album, title, track)`, with indexes on `hash` and `(artist, album)`. It was filled with 50,000 synthetic rows shaped like Windows music paths, with 5% duplicate hashes.
- **Build:** `CGO_ENABLED=0 go build -tags <driver>`, plus a second build with `-ldflags "-s -w"` for the stripped sizes.
- **Concurrency test:** Three processes started at the same moment, each running for 10 seconds, then `PRAGMA integrity_check` on the file afterwards.
- **Environment:** Windows 11 Pro amd64 (10.0.26340), Go 1.27.1, local NTFS disk. The absolute timings are specific to this machine. The comparison between drivers is what matters.
