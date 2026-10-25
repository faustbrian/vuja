#!/bin/bash
# Run only on a disposable hosted runner, after protected release publication.
set -Eeuo pipefail
umask 077

stage=arguments
root=
cleanup() {
  local result=$?
  trap - EXIT
  if [[ "$result" != 0 ]]; then
    printf 'qualification failed at %s\n' "$stage" >&2
  fi
  if [[ -n "$root" ]]; then
    cd /
    # Module caches are read-only: go.dev/ref/mod#module-cache. Only this
    # mktemp-owned workspace is made writable and removed, even after failure.
    if ! chmod -R u+w "$root" || ! rm -rf -- "$root"; then
      printf 'cleanup failed\n' >&2
      result=1
    else
      printf 'cleanup passed\n'
    fi
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Explicit rejection also works with macOS /bin/bash 3.2, whose errexit can
# continue after a failed top-level [[ ... ]] compound command.
[[ $# == 3 || ( $# == 4 && $4 == --go-install ) ]] || exit 1
tag=$1
source_sha=$2
platform=$3
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]] || exit 1
[[ "$source_sha" =~ ^[0-9a-f]{40}$ ]] || exit 1
case "$platform" in linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;; *) exit 1;; esac
[[ ${GITHUB_ACTIONS-} == true && ${RUNNER_ENVIRONMENT-} == github-hosted ]] || exit 1
[[ -n ${RUNNER_TEMP-} && -d "$RUNNER_TEMP" ]] || exit 1

stage=native-platform
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) native=linux/amd64;;
  Linux/aarch64|Linux/arm64) native=linux/arm64;;
  Darwin/x86_64) native=darwin/amd64;;
  Darwin/arm64) native=darwin/arm64;;
  *) exit 1;;
esac
[[ "$native" == "$platform" ]] || exit 1
printf 'release: %s source: %s native: %s\n' "$tag" "$source_sha" "$native"

stage=private-state
root=$(mktemp -d "$RUNNER_TEMP/vuja-qualification.XXXXXX")
readonly root
for variable in "${!VUJA_@}" "${!GO@}"; do unset "$variable"; done
unset BASH_ENV ENV CDPATH CODEX_HOME CLAUDE_CONFIG_DIR TMUX TMUX_PANE \
  ITERM_SESSION_ID TERM_SESSION_ID GHOSTTY_RESOURCES_DIR GHOSTTY_BIN_DIR
export HOME="$root/archive/home" ZDOTDIR="$root/archive/home" TMPDIR="$root/tmp"
export XDG_CONFIG_HOME="$root/archive/config" XDG_DATA_HOME="$root/archive/data"
export XDG_CACHE_HOME="$root/archive/cache" XDG_RUNTIME_DIR="$root/runtime"
export GH_CONFIG_DIR="$root/gh" GH_HOST=github.com GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1
export VUJA_IS_CHILD=true VUJA_UPDATER_CHECK_ON_STARTUP=false VUJA_AI_ENABLED=false
mkdir -p "$HOME" "$TMPDIR" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" \
  "$XDG_CACHE_HOME" "$XDG_RUNTIME_DIR" "$GH_CONFIG_DIR" "$root/assets" "$root/archive/bin"
cd "$root"

stage=download
asset="vuja_${platform//\//_}.tar.gz"
gh release download "$tag" --repo faustbrian/vuja --pattern "$asset" \
  --pattern SHA256SUMS --dir "$root/assets"
[[ -s "$root/assets/$asset" && -s "$root/assets/SHA256SUMS" ]] || exit 1

stage=checksum
expected=$(awk -v asset="$asset" '$2 == asset { count++; digest=$1 } END { if (count != 1) exit 1; print digest }' "$root/assets/SHA256SUMS")
[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || exit 1
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$root/assets/$asset")
else
  actual=$(shasum -a 256 "$root/assets/$asset")
fi
actual=${actual%% *}
[[ "$actual" == "$expected" ]] || exit 1
printf 'sha256: %s %s\n' "$actual" "$asset"

stage=provenance
# gh verifies the subject digest/signature; these policy flags bind the
# authenticated source and signer, not unsigned release metadata.
# https://cli.github.com/manual/gh_attestation_verify
gh attestation verify "$root/assets/$asset" --repo faustbrian/vuja \
  --signer-workflow faustbrian/vuja/.github/workflows/release.yml \
  --signer-digest "$source_sha" --source-digest "$source_sha" \
  --source-ref "refs/tags/$tag" --deny-self-hosted-runners --format json
unset GH_TOKEN GITHUB_TOKEN

stage=archive-install
contents=$(tar -tzf "$root/assets/$asset" | LC_ALL=C sort)
[[ "$contents" == $'LICENSE\nTHIRD_PARTY_NOTICES.md\nvuja' ]] || exit 1
# Stream named regular members instead of honoring archive paths/links.
tar -tvzf "$root/assets/$asset" | awk 'substr($0, 1, 1) != "-" { exit 1 }'
for member in vuja LICENSE THIRD_PARTY_NOTICES.md; do
  tar -xOf "$root/assets/$asset" "$member" > "$root/archive/$member"
  [[ -s "$root/archive/$member" ]] || exit 1
done
install -m 0755 "$root/archive/vuja" "$root/archive/bin/vuja"

stage=archive-version
version=$("$root/archive/bin/vuja" version)
[[ "$version" == "vuja $tag" ]] || exit 1
printf 'archive version: %s\n' "$version"

if [[ $# == 4 ]]; then
  stage=module-install
  # Resolve the published /v2 module outside the checkout with independent
  # empty caches; no checkout replacements or linker version overrides.
  export HOME="$root/module/home" ZDOTDIR="$root/module/home"
  export XDG_CONFIG_HOME="$root/module/config" XDG_DATA_HOME="$root/module/data"
  export XDG_CACHE_HOME="$root/module/cache" GOPATH="$root/module/gopath"
  export GOBIN="$root/module/bin" GOCACHE="$root/module/build-cache"
  export GOMODCACHE="$root/module/mod-cache" GOTMPDIR="$root/module/tmp"
  export GOENV=off GOWORK=off GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct
  mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" \
    "$GOBIN" "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR" "$root/module/work"
  cd "$root/module/work"
  go install "github.com/faustbrian/vuja/v2/cmd/vuja@$tag"
  stage=module-metadata
  go version -m "$GOBIN/vuja" | tee "$root/module/build-info.txt"
  awk '$1 == "path" && $2 == "github.com/faustbrian/vuja/v2/cmd/vuja" { found=1 } END { exit !found }' "$root/module/build-info.txt"
  awk -v tag="$tag" '$1 == "mod" && $2 == "github.com/faustbrian/vuja/v2" && $3 == tag { found=1 } END { exit !found }' "$root/module/build-info.txt"
  stage=module-version
  version=$("$GOBIN/vuja" version)
  [[ "$version" == "vuja $tag" ]] || exit 1
  printf 'module version: %s\n' "$version"
fi
printf 'qualification passed: %s %s %s\n' "$tag" "$source_sha" "$platform"
