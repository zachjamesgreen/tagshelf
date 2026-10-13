#!/bin/sh
# Runs the tagshelf binary beside this script through a small library, to
# show that it works on the system it is run on. It is run inside a container
# of each Linux distribution by distros.sh.
#
# Usage: smoke.sh <folder of the test audio> [version the binary should say]
#
# Plain sh: Alpine has no bash.
set -eu

bin="$(cd "$(dirname "$0")" && pwd)/tagshelf"
audio="$(cd "$1" && pwd)"
version="${2:-}"

fail() {
  echo "FAILED: $*" >&2
  exit 1
}

work="$(mktemp -d)"
# tagshelf reads a config.yaml in the folder it is started in: it is started
# in an empty one, wherever this script was.
mkdir -p "$work/start" "$work/src/Rip"
cd "$work/start"
cp "$audio/tagged.flac" "$audio/tagged.mp3" "$work/src/Rip/"
cp "$audio/untagged.ogg" "$work/src/"

echo "--- the version, and the help"
got="$("$bin" -version)"
echo "$got"
if [ -n "$version" ] && [ "$got" != "tagshelf $version" ]; then
  fail "the binary says '$got', not 'tagshelf $version'"
fi
"$bin" | grep -q 'tagshelf -source ~/Incoming -dest ~/Music' || fail "the help does not show paths as Linux writes them"

echo "--- a dry run moves nothing"
"$bin" -source "$work/src" -dest "$work/lib" -dry-run > "$work/dry.log" 2>&1 || { cat "$work/dry.log"; fail "the dry run"; }
[ -f "$work/src/Rip/tagged.flac" ] || fail "the dry run moved a file"
[ ! -e "$work/lib" ] || fail "the dry run made the library folder"

echo "--- a run organizes the three files"
"$bin" -source "$work/src" -dest "$work/lib" > "$work/run.log" 2>&1 || { cat "$work/run.log"; fail "the run"; }
album="$work/lib/Fixture Album Artist/Fixture Album (2001)"
for file in "$album/1-03 - Silence Café.flac" "$album/1-03 - Silence Café.mp3" "$work/lib/_Untagged/untagged.ogg"; do
  [ -f "$file" ] || { cat "$work/run.log"; find "$work/lib"; fail "$file is not there"; }
done
grep -q 'Errors encountered:  *0$' "$work/run.log" || { cat "$work/run.log"; fail "the run had errors"; }
if grep -q 'Warning' "$work/run.log"; then
  cat "$work/run.log"
  fail "the run had warnings: the tag reader may not have read the files"
fi

echo "--- the index, and a second run that finds everything in place"
"$bin" -index -dest "$work/lib" > "$work/index.log" 2>&1 || { cat "$work/index.log"; fail "the index run"; }
"$bin" -reorganize "$work/lib" > "$work/again.log" 2>&1 || { cat "$work/again.log"; fail "the reorganize"; }
grep -q 'Already in place:  *2$' "$work/again.log" || { cat "$work/again.log"; fail "the reorganize did not find the two tagged files in place"; }

echo "--- the report lists the three files"
"$bin" report -dest "$work/lib" -o "$work/report.csv" > "$work/report.log" 2>&1 || { cat "$work/report.log"; fail "the report"; }
[ "$(wc -l < "$work/report.csv")" -eq 4 ] || { cat "$work/report.csv"; fail "the report does not have a row for each of the three files"; }
grep -q 'Files:  *3 (library 2, untagged 1)$' "$work/report.log" || { cat "$work/report.log"; fail "the report's counts"; }

echo "--- its data is under the home folder"
ls "$HOME/.local/share/tagshelf/libraries" > /dev/null || fail "no database under ~/.local/share/tagshelf"
ls "$HOME/.cache/tagshelf" > /dev/null || fail "no cache under ~/.cache/tagshelf"

echo "--- a Windows path is refused"
code=0
"$bin" -source 'D:\Incoming' -dest "$work/other" > "$work/refused.log" 2>&1 || code=$?
[ "$code" = 2 ] || { cat "$work/refused.log"; fail "a Windows path gave exit code $code, not 2"; }
grep -q 'is a Windows path' "$work/refused.log" || { cat "$work/refused.log"; fail "the refusal does not say why"; }

rm -rf "$work"
echo "ok"
