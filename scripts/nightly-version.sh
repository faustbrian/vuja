#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || ! "$2" =~ ^[0-9a-f]+$ ]]; then
  echo "usage: nightly-version.sh [v2 stable or candidate base] git-short-sha" >&2
  exit 1
fi

base=$1
next=v2.0.0
if [[ -n "$base" ]]; then
  if [[ ! "$base" =~ ^v2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]]; then
    echo "nightly base must be a v2 stable or release-candidate tag" >&2
    exit 1
  fi
  minor=${BASH_REMATCH[1]}
  patch=${BASH_REMATCH[2]}
  candidate=${BASH_REMATCH[3]}
  if [[ -z "$candidate" ]]; then
    # SemVer 2.0.0's numeric-identifier grammar has no machine-integer bound
    # (semver.org/#backusnaur-form-grammar-for-valid-semver-versions). Increment
    # decimal digits rather than overflowing Bash's signed arithmetic.
    incremented=
    carry=1
    while [[ -n "$patch" ]]; do
      digit=${patch: -1}
      patch=${patch%?}
      digit=$((digit + carry))
      carry=$((digit / 10))
      incremented="$((digit % 10))$incremented"
    done
    patch="${carry#0}$incremented"
  fi
  next="v2.$minor.$patch"
fi

short=$2
# SemVer 2.0.0 section 9 forbids leading zeroes in numeric prerelease IDs.
# Keep ordinary hashes unchanged; make this rare numeric partition nonnumeric.
if [[ "$short" =~ ^0[0-9]+$ ]]; then
  short="g$short"
fi
printf '%s-nightly.%s\n' "$next" "$short"
