# The fingerprint module

`../fp.wasm` is the code tagshelf identifies audio by its sound with. It is one WebAssembly module, built from three things:

| Part | Version | Licence | What it does here |
|---|---|---|---|
| [FFmpeg](https://ffmpeg.org/) | 7.1.1 | LGPL 2.1 or later | Reads the file and decodes its audio. |
| [Chromaprint](https://acoustid.org/chromaprint) | 1.5.1 | MIT, with a resampler from FFmpeg under the LGPL 2.1 or later | Turns the audio into an AcoustID fingerprint. |
| `fp.cc`, in this folder | | MIT, as tagshelf | Joins the two, the way AcoustID's own tool `fpcalc` does. |

It is linked with the C and C++ libraries of the [WASI SDK](https://github.com/WebAssembly/wasi-sdk) 34 (wasi-libc and LLVM's libc++, under the MIT and Apache 2.0 licences, the latter with the LLVM exception).

tagshelf carries the module inside its executable and runs it with [wazero](https://wazero.io/). Nothing is installed or looked for on your machine. The module is given one folder at a time, the folder of the file it listens to, and can only read it. It has no network.

## What of FFmpeg is in it

Only audio, and only parts under the LGPL: FFmpeg is configured without `--enable-gpl` and without `--enable-nonfree`, and no outside library is linked. `build.sh` stops if FFmpeg's `configure` reports any other licence than "LGPL version 2.1 or later".

- **Decoders:** every audio decoder FFmpeg has of its own (199 of them), listed from FFmpeg's `libavcodec/allcodecs.c` by `build.sh`.
- **Demuxers and parsers:** those of the audio formats, named in `build.sh`.
- **Nothing else:** no video, no encoders, no filters, no network, no devices.

FFmpeg and Chromaprint are used unchanged. The exact `configure` line is in `build.sh`.

## Building it

```sh
internal/fingerprint/module/build.sh /some/work/folder
```

This needs Linux on x86-64 with bash, curl, tar, xz, make, python3 and sha256sum. It downloads the WASI SDK and the two sources (about 200 MB), checks each against the SHA-256 written in `build.sh`, builds, and writes `internal/fingerprint/fp.wasm`. It takes a minute or two.

The build is reproducible: the same `build.sh` gives the same file, byte for byte, on any machine. The workflow `.github/workflows/fingerprint-module.yml` builds the module again whenever this folder changes and fails when the file in the repository is not what it built.

## The sources

The sources are the release archives of the two projects, unchanged:

| Archive | SHA-256 |
|---|---|
| <https://ffmpeg.org/releases/ffmpeg-7.1.1.tar.xz> | `733984395e0dbbe5c046abda2dc49a5544e7e0e1e2366bba849222ae9e3a03b1` |
| <https://github.com/acoustid/chromaprint/releases/download/v1.5.1/chromaprint-1.5.1.tar.gz> | `a1aad8fa3b8b18b78d3755b3767faff9abb67242e01b478ec9a64e190f335e1c` |

Each run of the workflow also keeps the two archives with the module it built, as an artifact named `fingerprint-module`.

## Using your own build

The LGPL gives you the right to run tagshelf with a build of FFmpeg and Chromaprint of your own. Build a module as above, from whatever versions or changes you like, and name it in the environment:

```sh
TAGSHELF_FINGERPRINT_MODULE=/path/to/fp.wasm tagshelf tui
```

tagshelf then runs that module in place of the one it carries. A module must be a WASI program that takes a file's path and a number of seconds as its arguments and prints `DURATION=` and `FINGERPRINT=` lines, as `fp.cc` does.

## How close it is to fpcalc

For the same file the module gives the fingerprint `fpcalc` 1.5.1 gives, bit for bit, for MP3, FLAC, AAC, ALAC, Opus, WAV, WavPack, AC-3 and TTA. For Ogg Vorbis and WMA the two differ in 3 to 4 bits of a hundred, because FFmpeg has changed those decoders since the FFmpeg 4.4 that `fpcalc` 1.5.1 was built with; AcoustID takes such fingerprints for the same audio. A native build of `fp.cc` against FFmpeg 7.1.1 gives exactly what the module gives.
