#!/bin/bash
# Runs a Linux build of tagshelf inside a container of each of the major
# distributions, as a user that is not root, with smoke.sh. The binary is
# static, so one build should run on all of them: this shows that it does.
#
# Usage: distros.sh <tagshelf binary> [version the binary should say]
#
# Run from the root of the repository. It needs Docker.
set -euo pipefail

binary="$1"
version="${2:-}"

# The images are the current release of each: what is checked is that
# tagshelf runs on the distributions as they are now.
images=(
  "debian:stable-slim"
  "fedora:latest"
  "archlinux:latest"
  "opensuse/leap:latest"
  "alpine:latest"
)

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cp "$binary" "$stage/tagshelf"
cp "$(dirname "$0")/smoke.sh" "$stage/smoke.sh"
chmod 755 "$stage" "$stage/tagshelf" "$stage/smoke.sh"

failed=()
for image in "${images[@]}"; do
  echo "::group::$image"
  if docker run --rm --user 1000:1000 -e HOME=/tmp/home \
    -v "$stage:/tagshelf:ro" -v "$PWD/testdata/audio:/audio:ro" \
    "$image" sh /tagshelf/smoke.sh /audio "$version"; then
    echo "$image: ok"
  else
    echo "::error::tagshelf does not run on $image"
    failed+=("$image")
  fi
  echo "::endgroup::"
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo "Failed on: ${failed[*]}"
  exit 1
fi
echo "tagshelf runs on all ${#images[@]} distributions."
