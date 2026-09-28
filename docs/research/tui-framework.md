# Go TUI framework for `tagshelf tui`

Researched: 2026-09-27

Question: Which Go TUI framework should `tagshelf tui` use? The planned screen runs the organizer with live progress, shows lists or tables of duplicate files for review, and has a form for editing audio tags plus a MusicBrainz search. Windows (Windows Terminal) is the primary platform.

Method: I used only primary sources: the official GitHub repositories (READMEs, `go.mod` files, source code) and their release pages. I read repo and release metadata from the public GitHub REST API (`api.github.com/repos/<owner>/<repo>` and `/releases`) on 2026-09-27. Star counts are rounded.

## Summary

**Recommendation: Bubble Tea v2 (`charm.land/bubbletea/v2`), with Bubbles v2 for the table, list, progress and spinner, Huh v2 for the tag-editor form, and Lip Gloss v2 for layout and styling.**

tview is the other serious option, and a reasonable one. It has richer built-in widgets (a ready `Table`, `List`, `Form`, `TreeView`), but it has not published a release in over a year and has no test suite of its own. Bubble Tea is better at the two things this project needs most: streaming progress from background work (the organizer) and deterministic tests. gocui is effectively unmaintained.

**Required change either way:** every maintained option now needs a newer Go than tagshelf's `go 1.21`. Bubble Tea v2 needs `go 1.26.0`, and tview `master` needs `go 1.25.0`. The local toolchain is go1.27.1 with `GOTOOLCHAIN=auto`, so bumping the `go` directive costs nothing locally.

## Comparison

| | Bubble Tea v2 + Bubbles + Huh + Lip Gloss | tview (on tcell v2) | gocui |
|---|---|---|---|
| Latest release | bubbletea v2.0.10, 2026-09-24; bubbles v2.2.1, 2026-08-24; lipgloss v2.0.6, 2026-08-11; huh v2.0.3, 2026-03-10 | v0.42.0, 2025-08-27 (only release); last commit 2026-08-11 | jroimartin: last push 2025-05-01, no releases; awesome-gocui fork: last release v1.1.0, 2022-01-13 |
| Release cadence | Roughly monthly patch releases | Rare tags; the author expects you to track `master` | Stale |
| Minimum Go | 1.26.0 (bubbletea), 1.25.x (bubbles, huh, lipgloss) | 1.18 at v0.42.0; 1.25.0 on `master` | n/a |
| Windows | Own console setup with VT input/output modes, via `x/windows` | Through tcell v2, which uses the VT console API and treats Windows Terminal as its main target | Through tcell |
| Table / list | Bubbles `table`, `list` (filterable), `paginator`, `viewport` | Built-in `Table` (selectable, navigable), `List`, `TreeView` | Hand-built views only |
| Form | Huh: `Input`, `Text`, `Select`, `Confirm`, per-field `Validate`; a `huh.Form` is a `tea.Model` | Built-in `Form` (inputs, dropdowns, checkboxes, buttons) | None |
| Progress from background work | `tea.Cmd` returns a `Msg`; `Program.Send` is safe from any goroutine; Bubbles `progress` and `spinner` | `QueueUpdateDraw` from any goroutine; no progress widget | Manual |
| Testability | Pure `Update(msg)`/`View()` functions; `WithInput`/`WithOutput`/`WithWindowSize`; `teatest` golden-output helper | Inject a tcell `SimulationScreen`; the repo has no `_test.go` files | Poor |

## Bubble Tea v2 (Charm)

### Maintenance

- Bubble Tea v2.0.0 was released 2026-02-24. The latest release is v2.0.10 (2026-09-24), after v2.0.9 (2026-08-19), v2.0.8 (2026-07-03) and v2.0.7 (2026-06-01). The repo was last pushed 2026-09-24, is not archived, and has about 45k stars. ([releases](https://github.com/charmbracelet/bubbletea/releases), [v2.0.0](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0))
- The companion libraries are also active:
  - Bubbles v2.2.1 (2026-08-24). ([releases](https://github.com/charmbracelet/bubbles/releases))
  - Lip Gloss v2.0.6 (2026-08-11). ([releases](https://github.com/charmbracelet/lipgloss/releases))
  - Huh v2.0.3 (2026-03-10); its repo was last pushed 2026-09-01. ([releases](https://github.com/charmbracelet/huh/releases))
- v2 moved to vanity import paths: `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2` and `charm.land/huh/v2`. ([bubbletea go.mod](https://github.com/charmbracelet/bubbletea/blob/v2.0.10/go.mod), [bubbles go.mod](https://github.com/charmbracelet/bubbles/blob/v2.2.1/go.mod), [huh go.mod](https://github.com/charmbracelet/huh/blob/v2.0.3/go.mod))
- Go version requirements:
  - bubbletea v2.0.10 declares `go 1.26.0`. ([go.mod](https://github.com/charmbracelet/bubbletea/blob/v2.0.10/go.mod))
  - bubbles v2.2.1 and lipgloss v2.0.6 declare `go 1.25.0`, and huh v2.0.3 declares `go 1.25.8`. (go.mod links above)
  - The last v1 release, v1.3.10 (2025-09-17), already declares `go 1.24.0`. Staying on v1 would therefore not avoid a Go bump. ([v1.3.10 go.mod](https://github.com/charmbracelet/bubbletea/blob/v1.3.10/go.mod))

### Windows Terminal

- `tty_windows.go` turns on `ENABLE_VIRTUAL_TERMINAL_INPUT` for the console input handle and `ENABLE_VIRTUAL_TERMINAL_PROCESSING | DISABLE_NEWLINE_AUTO_RETURN` for the output handle, using `github.com/charmbracelet/x/windows`. ([tty_windows.go](https://github.com/charmbracelet/bubbletea/blob/main/tty_windows.go), [go.mod](https://github.com/charmbracelet/bubbletea/blob/v2.0.10/go.mod))
- The v2.0.0 release notes list the Windows Console API as one of the two input sources for richer key events (for example `key.IsRepeat`). ([v2.0.0 notes](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0))

### Widgets

- Bubbles includes Spinner, Text Input, Text Area, Table, Progress, Paginator, Viewport, List, File Picker, Timer, Stopwatch, Help, Key, and a `tree` package. ([README](https://github.com/charmbracelet/bubbles/blob/main/README.md), [repo tree](https://github.com/charmbracelet/bubbles))
  - For the duplicates screen, `table` fits a flat review grid (path, size, bitrate, tags), and `list` gives a filterable list.
- Huh provides `NewInput`, `NewText`, `NewSelect[T]` and `NewConfirm`, each with `Validate(func) error`. That covers title, artist, album, track, year and genre fields, and a Select can hold MusicBrainz results. It also has an accessible mode for screen readers. ([huh README](https://github.com/charmbracelet/huh/blob/main/README.md))
- Huh states that "a `huh.Form` is just a `tea.Model`", so a form can be embedded in a larger Bubble Tea program. ([huh README, "What about Bubble Tea?"](https://github.com/charmbracelet/huh/blob/main/README.md#what-about-bubble-tea))

### Background work and progress

- The Elm-style loop has `Init` and `Update` return a `tea.Cmd`. A Cmd runs I/O off the UI loop and returns a `Msg`. ([README](https://github.com/charmbracelet/bubbletea/blob/main/README.md), [commands tutorial](https://github.com/charmbracelet/bubbletea/tree/main/tutorials/commands/))
- `Program.Send(msg)` injects messages from outside the program. After the program exits it is a no-op, "so it's safe to send messages after the program has exited". ([tea.go](https://github.com/charmbracelet/bubbletea/blob/main/tea.go))
  - This means the existing organizer loop in `main.go` can run in its own goroutine and call `p.Send(progressMsg{...})` per file, with no UI code inside the organizer.
- The official examples cover this exact pattern: `progress-download`, `progress-bar`, `realtime`, `send-msg`, `package-manager`, `table` and `list-*`. ([examples](https://github.com/charmbracelet/bubbletea/tree/main/examples))

### Testability

- Because `Update` and `View` are plain functions over a model value, you can unit-test them without a terminal. ([README](https://github.com/charmbracelet/bubbletea/blob/main/README.md))
- Program options include `WithInput`, `WithOutput`, `WithContext`, `WithoutRenderer`, `WithoutSignals` and `WithWindowSize`. ([options.go](https://github.com/charmbracelet/bubbletea/blob/main/options.go))
  - The v2.0.0 notes say `WithWindowSize` exists so there are "No more mocking terminals just to run your tests". ([v2.0.0 notes](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0))
- `teatest/v2` provides `NewTestModel`, `Send`, `Type`, `WaitFor`, `FinalModel`, `FinalOutput` and `RequireEqualOutput` (golden files). ([teatest.go](https://github.com/charmbracelet/x/blob/main/exp/teatest/v2/teatest.go))
  - Caveat: it lives under `charmbracelet/x/exp` (experimental), and its go.mod pins `charm.land/bubbletea/v2 v2.0.0-rc.1`. ([go.mod](https://github.com/charmbracelet/x/blob/main/exp/teatest/v2/go.mod))

### Costs

- You write more code than with tview: focus handling between panes, and layout with Lip Gloss, are your job.
- A tview-style `Table` with per-cell selection is not built in. Bubbles `table` selects rows.

## tview (rivo/tview)

### Maintenance

- There is one GitHub release, v0.42.0 (2025-08-27). The last commit on `master` was 2026-08-11. The repo has about 14k stars and is not archived. ([releases](https://github.com/rivo/tview/releases), [commits](https://github.com/rivo/tview/commits/master))
- The README promises strong backwards compatibility instead of semver releases. ([README, "Backwards-Compatibility"](https://github.com/rivo/tview/blob/master/README.md))
- Go version requirements:
  - v0.42.0 declares `go 1.18` and `tcell/v2 v2.8.1`. ([go.mod@v0.42.0](https://github.com/rivo/tview/blob/v0.42.0/go.mod))
  - `master` declares `go 1.25.0` and is still on `tcell/v2`. ([go.mod@master](https://github.com/rivo/tview/blob/master/go.mod))
  - Only the old tag would work with tagshelf's current Go 1.21, and it lags a year of fixes.
- tview depends on tcell. tcell is now v3 (v3.5.0, 2026-09-11) and says v2 "remains available". The v2 branch still receives tags (latest v2.13.10), but tview has not moved to v3. ([tcell README](https://github.com/gdamore/tcell/blob/main/README.md), [tcell releases](https://github.com/gdamore/tcell/releases))

### Windows Terminal

- tcell supports Windows 10 1703 and later. It uses "the modern VT API ... including the win32-input-mode". It names Windows Terminal as its preferred emulator on Windows 11 and "the main test target we use". ([README-windows.md](https://github.com/gdamore/tcell/blob/main/README-windows.md))
- This is tcell's own statement. tview itself makes no separate Windows claim.

### Widgets

- The README lists input forms (text input, selections, checkboxes, buttons), text areas, "sophisticated navigable table views", selectable lists, tree views, modals, and Flex/Grid layout. There are demos for `table`, `list`, `form`, `treeview`, `modal` and `pages`. ([README](https://github.com/rivo/tview/blob/master/README.md), [demos](https://github.com/rivo/tview/tree/master/demos))
- This is the strongest out-of-the-box widget set of the options. Both the duplicates table and the tag form are nearly free.

### Background work and progress

- The docs say "Many functions in this package are not thread-safe". ([doc.go, "Concurrency"](https://github.com/rivo/tview/blob/master/doc.go))
- Other goroutines must go through `QueueUpdate(f)`, which blocks until `f` has run on the event loop. `QueueUpdateDraw(f)` does the same and then redraws. ([application.go](https://github.com/rivo/tview/blob/master/application.go))
- This works, but the organizer would have to call into tview widgets through closures, and it blocks on the UI loop. There is also no progress-bar widget.

### Testability

- `Application.SetScreen(tcell.Screen)` lets you inject tcell's `SimulationScreen` (`NewSimulationScreen`). ([application.go](https://github.com/rivo/tview/blob/master/application.go), [tcell v2 simulation.go](https://github.com/gdamore/tcell/blob/v2/simulation.go))
- Widget state is mutable and spread across objects, and the tview repo contains no `_test.go` files at its root. ([repo tree](https://github.com/rivo/tview))

## Other options

- **gocui**:
  - `jroimartin/gocui` was last pushed 2025-05-01 and has no releases. ([repo](https://github.com/jroimartin/gocui))
  - The `awesome-gocui/gocui` fork's latest release is v1.1.0 from 2022-01-13. ([releases](https://github.com/awesome-gocui/gocui/releases))
  - It has no table, list or form widgets. Not recommended.
- **Raw tcell v3**: this is actively maintained ([releases](https://github.com/gdamore/tcell/releases)), but it is a screen and cell library, not a widget toolkit. It would mean building every widget by hand.

## Recommendation

Use **Bubble Tea v2 + Bubbles v2 + Huh v2 + Lip Gloss v2**, and raise tagshelf's `go` directive to at least 1.26.

Reasons:

1. **Maintenance:** Bubble Tea and its companion libraries ship roughly monthly releases on a stable v2 line (2.0.0 in Feb 2026, 2.0.10 in Sep 2026). tview has one release in a year, and it still sits on tcell v2 while tcell has moved to v3.
2. **Background work:** `Program.Send` is goroutine-safe, so the existing organizer can report progress by sending messages without importing UI code. Bubbles `progress`/`spinner` and the official `progress-download`/`package-manager` examples match the "organize with live progress" screen directly.
3. **Testability:** `Update` and `View` are pure. `WithInput`, `WithOutput` and `WithWindowSize` plus `teatest` golden files make headless tests practical. tview testing means driving a simulation screen through mutable widget objects.
4. **Widgets are sufficient:** Bubbles `table`/`list` covers the duplicate review, and Huh covers the tag editor, including a `Select` populated from a MusicBrainz search and field validation.
5. **Windows:** both stacks explicitly configure the Windows console VT modes. tcell's documented Windows Terminal focus is a point for tview, but not a decisive one.

When tview would be the better choice: if minimizing UI code matters more than testability and streaming progress, or if the duplicates view needs per-cell editing in a grid. Its built-in `Table` and `Form` would ship faster.

Caveats:

- `teatest` is under `x/exp`, so its API may change.
- Bubble Tea is more code per screen: focus management and layout are yours to write.
