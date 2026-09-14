#!/usr/bin/env bash
# Stamp the build and signed manifest with the same release identity.
set -euo pipefail

if [[ "${GITHUB_EVENT_NAME:-}" != push ]]; then
  echo "Releases require a push event" >&2
  exit 1
fi

if [[ "$GITHUB_REF" == refs/heads/main ]]; then
  [[ "$GITHUB_RUN_NUMBER" =~ ^[1-9][0-9]*$ ]]
  base="$(go run ./cmd/version -check -format base)"
  current="$(go run ./cmd/version -check)"
  if [[ "$current" != "$base-dev"* && "$current" != "$base-main."* ]]; then
    # A commit intentionally tagged for release already has its identity.
    tag="v$current"
  else
    tag="v${base}-main.${GITHUB_RUN_NUMBER}"
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
grep -Fxq "VOICX_VERSION=${tag#v}" <<< "$metadata"
grep -Fxq "VOICX_DIRTY=false" <<< "$metadata"
printf '%s\n' "$metadata" >> "$GITHUB_ENV"
printf 'tag=%s\n' "$tag" >> "$GITHUB_OUTPUT"
grep '^VOICX_PRERELEASE=' <<< "$metadata" | sed 's/VOICX_PRERELEASE=/prerelease=/' >> "$GITHUB_OUTPUT"
