#!/usr/bin/env bash
# Stamp the build and signed manifest with the same release identity.
set -euo pipefail

if [[ "${GITHUB_EVENT_NAME:-}" != push ]]; then
  echo "Releases require a push event" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "Releases require a clean checkout" >&2
  exit 1
fi

if [[ "$GITHUB_REF" == refs/heads/main ]]; then
  current="$(go run ./cmd/version -check)"
  if [[ "$current" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    # A rerun of a published commit keeps the same stable release identity.
    tag="v$current"
  else
    tag="v$(go run ./cmd/version -check -format next-release)"
    if git show-ref --verify --quiet "refs/tags/$tag"; then
      test "$(git rev-parse "$tag^{commit}")" = "$(git rev-parse HEAD)"
    else
      # This tag stays local until all binaries and signatures are verified.
      git tag "$tag" HEAD
    fi
  fi
elif [[ "$GITHUB_REF" == refs/tags/v* ]]; then
  tag="${GITHUB_REF#refs/tags/}"
else
  echo "Releases require main or a version tag" >&2
  exit 1
fi

metadata="$(go run ./cmd/version -check -format github)"
grep -Fxq "NOXA_VERSION=${tag#v}" <<< "$metadata"
grep -Fxq "NOXA_DIRTY=false" <<< "$metadata"
if [[ "$GITHUB_REF" == refs/heads/main ]]; then
  grep -Fxq "NOXA_PRERELEASE=false" <<< "$metadata"
fi
printf '%s\n' "$metadata" >> "$GITHUB_ENV"
printf 'tag=%s\n' "$tag" >> "$GITHUB_OUTPUT"
grep '^NOXA_PRERELEASE=' <<< "$metadata" | sed 's/NOXA_PRERELEASE=/prerelease=/' >> "$GITHUB_OUTPUT"
