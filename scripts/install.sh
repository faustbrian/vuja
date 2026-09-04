#!/bin/sh
set -e

REPO="faustbrian/vuja"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
# allow overriding the GitHub API base URL for local testing
VUJA_API_URL="${VUJA_API_URL:-https://api.github.com}"
VUJA_CHANNEL="${VUJA_CHANNEL:-stable}"

main() {
    validate_channel
    echo "Installing vuja..."

    arch=$(get_arch)
    echo "Detected architecture: ${arch}"

    tmp_dir=$(mktemp -d)
    trap 'rm -rf '"${tmp_dir}" EXIT
    cd "${tmp_dir}"

    asset_urls=$(get_download_urls "${arch}")
    download_url=$(printf '%s\n' "${asset_urls}" | sed -n '1p')
    checksum_url=$(printf '%s\n' "${asset_urls}" | sed -n '2p')
    if [ -z "${download_url}" ]; then
        err "Could not find release for architecture: ${arch}"
    fi
    if [ -z "${checksum_url}" ]; then
        err "Could not find SHA256SUMS for the release"
    fi
    echo "Downloading: ${download_url}"

    archive=$(basename "${download_url}")
    download_file "${download_url}" "${archive}"
    download_file "${checksum_url}" "SHA256SUMS"
    verify_checksum "${archive}" "SHA256SUMS"

    case "${archive}" in
        *.tar.gz) tar -xzf "${archive}" ;;
        *.zip)
            if command -v unzip >/dev/null 2>&1; then
                unzip -q "${archive}"
            else
                err "unzip is required to extract ${archive}"
            fi
            ;;
        *) err "Unknown archive format: ${archive}" ;;
    esac

    bin=$(find . -name "vuja" -type f | head -1)
    [ -z "$bin" ] && err "Binary not found in archive"
    chmod +x "$bin"
    if ! "$bin" version >/dev/null 2>&1; then
        err "Downloaded binary could not be executed on this system"
    fi

    # check if we have write permission to the install directory
    has_write_permission=0
    if [ -d "${BIN_DIR}" ]; then
        if [ -w "${BIN_DIR}" ]; then
            has_write_permission=1
        fi
    else
        parent_dir=$(dirname "${BIN_DIR}")
        if [ -w "${parent_dir}" ]; then
            has_write_permission=1
        fi
    fi

    if [ "${has_write_permission}" -eq 1 ]; then
        mkdir -p "${BIN_DIR}"
        install_binary "$bin" "${BIN_DIR}/vuja" || err "Could not replace ${BIN_DIR}/vuja"
        if "${BIN_DIR}/vuja" version >/dev/null 2>&1; then
            echo "Installation verified."
            echo ""
            "${BIN_DIR}/vuja" setup
        else
            echo "Warning: could not verify installed binary at ${BIN_DIR}/vuja"
        fi
    else
        # fallback to ~/.local/bin which is user-writable without sudo
        local_bin="${HOME}/.local/bin"
        mkdir -p "${local_bin}"
        install_binary "$bin" "${local_bin}/vuja" || err "Could not replace ${local_bin}/vuja"
        if "${local_bin}/vuja" version >/dev/null 2>&1; then
            echo "Installation verified."
            echo ""
            "${local_bin}/vuja" setup
        else
            # both locations failed, sudo install
            echo ""
            echo "Installation requires elevated permissions, enter your password:"
            if sudo_install_binary "$bin" "${BIN_DIR}/vuja"; then
                echo "Installation verified."
                echo ""
                "${BIN_DIR}/vuja" setup
            else
                tmp_vuja=$(mktemp "${TMPDIR:-/tmp}/vuja.XXXXXX")
                cp "$bin" "${tmp_vuja}"
                chmod +x "${tmp_vuja}"
                echo ""
                printf "Failed. Run manually: \033[32msudo cp %s %s/vuja\033[0m\n" "${tmp_vuja}" "${BIN_DIR}"
            fi
        fi
    fi
}

install_binary() {
    source_binary="$1"
    destination="$2"
    staged=$(mktemp "${destination}.tmp.XXXXXX") || return 1
    if ! cp "${source_binary}" "${staged}" || ! chmod 0755 "${staged}" || ! mv -f "${staged}" "${destination}"; then
        rm -f "${staged}" 2>/dev/null || true
        return 1
    fi
}

sudo_install_binary() {
    source_binary="$1"
    destination="$2"
    staged=$(sudo mktemp "${destination}.tmp.XXXXXX") || return 1
    if ! sudo cp "${source_binary}" "${staged}" || ! sudo chmod 0755 "${staged}" || ! sudo mv -f "${staged}" "${destination}"; then
        sudo rm -f "${staged}" 2>/dev/null || true
        return 1
    fi
}

get_arch() {
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)

    case "${os}" in
        linux)  os="linux" ;;
        darwin) os="darwin" ;;
        *) err "Unsupported OS: ${os}" ;;
    esac

    case "${arch}" in
        x86_64 | amd64)   arch="amd64" ;;
        aarch64 | arm64)  arch="arm64" ;;
        *) err "Unsupported architecture: ${arch}" ;;
    esac

    echo "${os}_${arch}"
}

get_download_urls() {
    arch="$1"
	endpoint="${VUJA_API_URL}/repos/${REPO}/releases/latest"
	if [ "${VUJA_CHANNEL}" != "stable" ]; then
		endpoint="${VUJA_API_URL}/repos/${REPO}/releases?per_page=100"
	fi

    if command -v curl >/dev/null 2>&1; then
        http_response=$(curl -sL -w "\n%{http_code}" \
            ${GITHUB_TOKEN:+-H "Authorization: Bearer ${GITHUB_TOKEN}"} \
            "${endpoint}")
        http_code=$(echo "${http_response}" | tail -1)
        releases=$(echo "${http_response}" | sed '$d')
    elif command -v wget >/dev/null 2>&1; then
        tmp_headers=$(mktemp)
        releases=$(wget -S -qO- \
            ${GITHUB_TOKEN:+--header "Authorization: Bearer ${GITHUB_TOKEN}"} \
            "${endpoint}" 2>"$tmp_headers" || true)
        http_code=$(grep "HTTP/" "$tmp_headers" | tail -1 | sed -e 's/^[[:space:]]*//' | cut -d' ' -f2)
        [ -z "${http_code}" ] && http_code="000"
        rm -f "$tmp_headers"
    else
        err "curl or wget is required"
    fi

    if [ "${http_code}" = "404" ]; then
        err "no releases found for ${REPO}. the project may not have published a release yet"
    fi

    if [ "${http_code}" = "403" ] || echo "${releases}" | grep -q "rate limit"; then
        err "GitHub API rate limited. try again later or set GITHUB_TOKEN env variable"
    fi

    if [ "${http_code}" != "200" ]; then
        msg=$(echo "${releases}" | grep '"message"' | head -1 | cut -d '"' -f 4)
        err "GitHub API error (HTTP ${http_code}): ${msg}"
    fi

	selected_release="${releases}"
	if [ "${VUJA_CHANNEL}" != "stable" ]; then
		selected_release=$(select_release_for_channel "${releases}" "${VUJA_CHANNEL}")
		[ -z "${selected_release}" ] && err "No eligible ${VUJA_CHANNEL} release found"
	fi
	selected_tag=$(extract_release_tag "${selected_release}")
	validate_selected_release "${selected_tag}" "${VUJA_CHANNEL}"

	archive_url=$(extract_release_asset_url "${selected_release}" "${arch}")
	checksum_url=$(extract_release_asset_url "${selected_release}" "SHA256SUMS")
    printf '%s\n%s\n' "${archive_url}" "${checksum_url}"
}

validate_channel() {
	case "${VUJA_CHANNEL}" in
		stable | rc | nightly) ;;
		*) err "VUJA_CHANNEL must be stable, rc, or nightly" ;;
	esac
}

select_release_for_channel() {
	json="$1"
	channel="$2"
	printf '%s' "${json}" | awk -v channel="${channel}" '
		BEGIN { RS = "\\\"tag_name\\\"[[:space:]]*:[[:space:]]*\\\"" }
		function newer_rc_candidate(candidate, current, candidate_parts, current_parts, position, candidate_stable, current_stable) {
			sub(/^v/, "", candidate)
			sub(/^v/, "", current)
			split(candidate, candidate_parts, /[.-]/)
			split(current, current_parts, /[.-]/)
			for (position = 1; position <= 3; position++) {
				if ((candidate_parts[position] + 0) > (current_parts[position] + 0)) { return 1 }
				if ((candidate_parts[position] + 0) < (current_parts[position] + 0)) { return 0 }
			}
			candidate_stable = candidate_parts[4] == ""
			current_stable = current_parts[4] == ""
			if (candidate_stable != current_stable) { return candidate_stable }
			return (candidate_parts[5] + 0) > (current_parts[5] + 0)
		}
		NR == 1 { next }
		{
			record = $0
			tag = record
			sub(/\".*/, "", tag)
			draft = record ~ /\"draft\"[[:space:]]*:[[:space:]]*true/
			prerelease = record ~ /\"prerelease\"[[:space:]]*:[[:space:]]*true/
			stable_tag = tag ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/
			rc_tag = tag ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$/
			nightly_tag = tag ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-nightly\.[0-9A-Za-z][0-9A-Za-z.-]*$/
			if (draft) { next }
			if (channel == "rc" && prerelease && rc_tag) {
				if (best_tag == "" || newer_rc_candidate(tag, best_tag)) {
					best_tag = tag
					best_record = record
				}
			}
			if (channel == "rc" && !prerelease && stable_tag) {
				if (best_tag == "" || newer_rc_candidate(tag, best_tag)) {
					best_tag = tag
					best_record = record
				}
			}
			if (channel == "nightly" && prerelease && nightly_tag) {
				print "\"tag_name\": \"" tag "\", " record
				exit
			}
		}
		END {
			if (channel == "rc" && best_tag != "") {
				print "\"tag_name\": \"" best_tag "\", " best_record
			}
		}
	'
}

extract_release_tag() {
	json="$1"
	printf '%s' "${json}" | awk '
		match($0, /\"tag_name\"[[:space:]]*:[[:space:]]*\"[^\"]+\"/) {
			tag = substr($0, RSTART, RLENGTH)
			sub(/^.*:[[:space:]]*\"/, "", tag)
			sub(/\"$/, "", tag)
			print tag
			exit
		}
	'
}

validate_selected_release() {
	tag="$1"
	channel="$2"
	stable_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
	rc_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'
	nightly_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-nightly\.[0-9A-Za-z][0-9A-Za-z.-]*$'
	case "${channel}" in
		stable)
			printf '%s\n' "${tag}" | grep -Eq "${stable_pattern}" || err "Stable release has invalid tag: ${tag}"
			;;
		rc)
			if ! printf '%s\n' "${tag}" | grep -Eq "${stable_pattern}|${rc_pattern}"; then
				err "Release-candidate channel selected an invalid tag: ${tag}"
			fi
			;;
		nightly)
			printf '%s\n' "${tag}" | grep -Eq "${nightly_pattern}" || err "Nightly release has invalid tag: ${tag}"
			;;
	esac
}

extract_release_asset_url() {
	json="$1"
	needle="$2"
	printf '%s' "${json}" | awk -v needle="${needle}" '
		BEGIN { RS = "\\\"browser_download_url\\\"[[:space:]]*:[[:space:]]*\\\"" }
		NR == 1 { next }
		{
			url = $0
			sub(/\".*/, "", url)
			if (index(url, needle) > 0) {
				print url
				exit
			}
		}
	'
}

download_file() {
    url="$1"
    destination="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${destination}" "${url}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "${destination}" "${url}"
    else
        err "curl or wget is required"
    fi
}

verify_checksum() {
    archive="$1"
    checksum_file="$2"
    expected=$(awk -v file="${archive}" '$2 == file || $2 == "*" file { print $1; exit }' "${checksum_file}")
    [ -z "${expected}" ] && err "No checksum found for ${archive}"

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "${archive}" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "${archive}" | awk '{print $1}')
    else
        err "sha256sum or shasum is required"
    fi

    [ "${actual}" = "${expected}" ] || err "Checksum mismatch for ${archive}"
    echo "Checksum verified."
}

err() {
    echo "Error: $1" >&2
    exit 1
}

main "$@"
