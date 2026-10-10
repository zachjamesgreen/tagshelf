# Contributing to tagshelf

Thank you for looking. tagshelf is a one-person project done in spare time, so please read this before spending yours.

## What is welcome

- **Bug reports.** Use the bug report form. The most useful ones say exactly what was run, what the tags of the file were, and where the file ended up.
- **Small fixes**, with a test.
- **Larger changes: open an issue first.** A new feature, a new audio format or a change to how files are organized should be agreed on before it is written. A large pull request that arrives unannounced may not be merged.

There is no promise of a quick reply.

## Setting up

You need Go 1.26 or newer on Windows. See [Building and testing](README.md#building-and-testing) in the README for the commands; CI runs the same ones.

## What a pull request needs

- **A test that fails without the change.** tagshelf moves people's files, so behaviour without a test is not taken on trust. Tests work on files in a temporary folder; none may touch real music, a real config file or the network.
- **`gofmt`, `go vet` and `staticcheck` clean**, and `go test ./...` passing on Windows.
- **Docs kept true.** If a flag, a config key or a screen changes, update `README.md` and `config.example.yaml`. If a screen of the terminal UI changes on purpose, run `go test ./internal/tui -update` and check the diff.
- **One change per pull request**, described in plain words: what it changes and why.

Pull requests are squashed into one commit on `main`, so the title should say what the change does.

## Things to keep to

- **Never delete or write over a person's file.** Set it aside, give the new one another name, or stop and say why.
- **Every move and every tag save is recorded**, so that it can be undone.
- **A dry run changes nothing** on disk.
- **Package boundaries.** `internal/engine` does not import `internal/tagwrite`; what it needs from there is passed in. `internal/tui` reads through its `Source` and writes through its `Runner`.
- **The project's words.** [CONTEXT.md](CONTEXT.md) defines them (library, run, qualifies, manual marker). Use those in code, messages and docs.
- **`internal/tag`** is a changed copy of another library. Record every change to it in [internal/tag/UPSTREAM.md](internal/tag/UPSTREAM.md), and do not change the audio hash of files that hash correctly today: the index stores those hashes.
- **Dependencies.** After adding or removing one, run `go run ./internal/notices` and commit `THIRD_PARTY_NOTICES`.

## Releases

Releases are made by the maintainer from the Actions tab: the Release workflow takes a version such as `v0.2.0`, tests `main`, builds `tagshelf.exe` and drafts the release.

## License

By contributing you agree that your contribution is released under the [MIT License](LICENSE).
