#!/bin/bash
# Builds fp.wasm, the module tagshelf identifies audio by its sound with:
# fp.cc, Chromaprint and the audio parts of FFmpeg, compiled to WebAssembly
# (WASI) and linked into one file.
#
# Usage: build.sh WORK [OUT]
#
# WORK is a folder for the downloads and the build, made if it is not there.
# OUT is where the module is written; without it, ../fp.wasm beside this
# script, which is the one tagshelf embeds.
#
# It needs Linux on x86-64 with bash, curl, tar, xz, make, python3 and
# sha256sum, and nothing else: the compiler is the WASI SDK it downloads.
# Every download is checked against the SHA-256 written here. Building twice
# gives the same file byte for byte, whatever WORK is.
#
# Only parts of FFmpeg under the LGPL are built: neither --enable-gpl nor
# --enable-nonfree is given, and no outside library is linked.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${1:?usage: build.sh WORK [OUT]}"
OUT_FILE="${2:-$HERE/../fp.wasm}"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd)"

WASI_SDK_URL=https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-34/wasi-sdk-34.0-x86_64-linux.tar.gz
WASI_SDK_SHA256=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4
FFMPEG_URL=https://ffmpeg.org/releases/ffmpeg-7.1.1.tar.xz
FFMPEG_SHA256=733984395e0dbbe5c046abda2dc49a5544e7e0e1e2366bba849222ae9e3a03b1
CHROMAPRINT_URL=https://github.com/acoustid/chromaprint/releases/download/v1.5.1/chromaprint-1.5.1.tar.gz
CHROMAPRINT_SHA256=a1aad8fa3b8b18b78d3755b3767faff9abb67242e01b478ec9a64e190f335e1c

# The demuxers and parsers of the audio formats. The decoders are every
# audio decoder FFmpeg has of its own, listed from its source below.
DEMUXERS=aac,ac3,aea,aiff,amr,ape,asf,ast,au,caf,dsf,dts,dtshd,eac3,flac,gsm,iff,loas,matroska,mlp,mov,mp3,mpc,mpc8,ogg,oma,rm,shorten,tak,truehd,tta,voc,w64,wav,wv,xa,xwma
PARSERS=aac,aac_latm,ac3,adx,amr,cook,dca,dolby_e,flac,g723_1,g729,gsm,mlp,mpegaudio,opus,sbc,sipr,tak,vorbis,xma

fetch() { # url sha256 file
  if [ ! -s "$3" ]; then
    curl -fL --retry 3 -sS -o "$3.part" "$1"
    mv "$3.part" "$3"
  fi
  echo "$2  $3" | sha256sum -c - > /dev/null || { echo "$3 is not the file expected: its SHA-256 differs" >&2; exit 1; }
}
unpack() { # archive dir
  rm -rf "$2"
  mkdir -p "$2"
  tar -xf "$1" -C "$2" --strip-components=1
}

mkdir -p "$WORK/dl"
fetch "$WASI_SDK_URL" "$WASI_SDK_SHA256" "$WORK/dl/wasi-sdk.tar.gz"
fetch "$FFMPEG_URL" "$FFMPEG_SHA256" "$WORK/dl/ffmpeg.tar.xz"
fetch "$CHROMAPRINT_URL" "$CHROMAPRINT_SHA256" "$WORK/dl/chromaprint.tar.gz"
[ -x "$WORK/wasi-sdk/bin/clang" ] || unpack "$WORK/dl/wasi-sdk.tar.gz" "$WORK/wasi-sdk"
unpack "$WORK/dl/ffmpeg.tar.xz" "$WORK/ffmpeg"
unpack "$WORK/dl/chromaprint.tar.gz" "$WORK/chromaprint"

SDK="$WORK/wasi-sdk"
# No path of this machine goes into the module.
MAP="-ffile-prefix-map=$WORK=."
EMULATED="-D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_PROCESS_CLOCKS -D_WASI_EMULATED_GETPID -D_WASI_EMULATED_MMAN"
EMULATED_LIBS="-lwasi-emulated-signal -lwasi-emulated-process-clocks -lwasi-emulated-getpid -lwasi-emulated-mman"

# The audio decoders, from the sections of allcodecs.c that hold them. Those
# that need an outside library or a system's own decoder are left out, as
# are the fixed-point twins of decoders that are built anyway.
DECODERS="$(cd "$WORK/ffmpeg" && python3 - <<'PY'
import re
audio, names = False, []
for line in open('libavcodec/allcodecs.c'):
    m = re.match(r'\s*/\*\s*(.*?)\s*\*/', line)
    if m:
        t = m.group(1).lower()
        if 'codecs' in t or 'external' in t or 'subtitles' in t or 'text' in t:
            audio = t in ('audio codecs', 'pcm codecs', 'dpcm codecs', 'adpcm codecs')
    m = re.match(r'extern const FFCodec ff_(\w+)_decoder;', line)
    if m and audio:
        names.append(m.group(1))
skip = re.compile(r'^(lib|.*_at$|.*_mf$|.*_mediacodec$|.*_fixed$)')
print(','.join(n for n in names if not skip.match(n)))
PY
)"
echo "FFmpeg: $(echo "$DECODERS" | tr ',' '\n' | wc -l) audio decoders"

# FFmpeg is built in its own source folder, so that the names of its files
# are written into it the same way everywhere.
cd "$WORK/ffmpeg"
./configure \
  --prefix="$WORK/out" \
  --enable-cross-compile --target-os=none --arch=wasm32 \
  --cc="$SDK/bin/clang" --cxx="$SDK/bin/clang++" --ld="$SDK/bin/clang" \
  --ar="$SDK/bin/llvm-ar" --ranlib="$SDK/bin/llvm-ranlib" --nm="$SDK/bin/llvm-nm" --strip="$SDK/bin/llvm-strip" \
  --extra-cflags="-Os $MAP $EMULATED" --extra-ldflags="$EMULATED_LIBS" \
  --disable-asm --disable-runtime-cpudetect --disable-autodetect --disable-pthreads --disable-w32threads --disable-os2threads \
  --disable-network --disable-programs --disable-doc --disable-debug --disable-stripping \
  --disable-avdevice --disable-avfilter --disable-swscale --disable-postproc \
  --disable-everything --enable-small --enable-protocol=file \
  --enable-decoder="$DECODERS" --enable-demuxer="$DEMUXERS" --enable-parser="$PARSERS" \
  --enable-static --disable-shared > "$WORK/ffmpeg-configure.log" 2>&1 || { tail -30 "$WORK/ffmpeg-configure.log" >&2; exit 1; }
grep '^License:' "$WORK/ffmpeg-configure.log"
grep -q '^License: LGPL version 2.1 or later$' "$WORK/ffmpeg-configure.log" || { echo "FFmpeg was not configured as LGPL 2.1 or later" >&2; exit 1; }
make -j"$(nproc)" > "$WORK/ffmpeg-make.log" 2>&1 || { grep -E 'error' "$WORK/ffmpeg-make.log" | head -20 >&2; exit 1; }
make install > "$WORK/ffmpeg-install.log" 2>&1

# Chromaprint, with its FFT taken from FFmpeg as AcoustID's own fpcalc has it.
B="$WORK/build"
rm -rf "$B"
mkdir -p "$B"
cd "$B"
C="$WORK/chromaprint/src"
FLAGS="-Os $MAP -fno-exceptions -fno-rtti -DUSE_AVFFT=1 -DHAVE_ROUND=1 -DHAVE_LRINTF=1 -DCHROMAPRINT_NODLL -D__STDC_CONSTANT_MACROS -D__STDC_LIMIT_MACROS $EMULATED -I$C -I$WORK/out/include"
OBJS=""
for s in audio_processor chroma chroma_filter chroma_resampler chromaprint fft fft_lib_avfft fingerprint_calculator \
  fingerprint_compressor fingerprint_decompressor fingerprinter fingerprinter_configuration image_builder \
  silence_remover simhash spectrum utils/base64; do
  o="$(echo "$s" | tr '/' '_').o"
  "$SDK/bin/clang++" $FLAGS -c "$C/$s.cpp" -o "$o"
  OBJS="$OBJS $o"
done
"$SDK/bin/clang" $FLAGS -c "$C/avresample/resample2.c" -o resample2.o
cp "$HERE/fp.cc" "$WORK/fp.cc"
"$SDK/bin/clang++" $FLAGS -c "$WORK/fp.cc" -o fp.o
"$SDK/bin/clang++" -Os -o fp.wasm fp.o $OBJS resample2.o \
  -L"$WORK/out/lib" -lavformat -lavcodec -lswresample -lavutil -lm $EMULATED_LIBS -Wl,--strip-all

mkdir -p "$(dirname "$OUT_FILE")"
cp fp.wasm "$OUT_FILE"
echo "built $OUT_FILE: $(wc -c < "$OUT_FILE") bytes, SHA-256 $(sha256sum "$OUT_FILE" | cut -d' ' -f1)"
