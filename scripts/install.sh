#!/bin/sh
set -e

REPO="faustbrian/vuja"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
# allow overriding the GitHub API base URL for local testing
VUJA_API_URL="${VUJA_API_URL:-https://api.github.com}"

main() {
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
        rm -f "${BIN_DIR}/vuja" 2>/dev/null || true
        cp "$bin" "${BIN_DIR}/vuja"
        chmod +x "${BIN_DIR}/vuja"
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
        chmod +x "$bin"
        rm -f "${local_bin}/vuja" 2>/dev/null || true
        cp "$bin" "${local_bin}/vuja"
        chmod +x "${local_bin}/vuja"
        if "${local_bin}/vuja" version >/dev/null 2>&1; then
            echo "Installation verified."
            echo ""
            "${local_bin}/vuja" setup
        else
            # both locations failed, sudo install
            echo ""
            echo "Installation requires elevated permissions, enter your password:"
            if sudo rm -f "${BIN_DIR}/vuja" 2>/dev/null && sudo cp "$bin" "${BIN_DIR}/vuja" && sudo chmod +x "${BIN_DIR}/vuja"; then
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

    if command -v curl >/dev/null 2>&1; then
        http_response=$(curl -sL -w "\n%{http_code}" \
            ${GITHUB_TOKEN:+-H "Authorization: Bearer ${GITHUB_TOKEN}"} \
            "${VUJA_API_URL}/repos/${REPO}/releases/latest")
        http_code=$(echo "${http_response}" | tail -1)
        releases=$(echo "${http_response}" | sed '$d')
    elif command -v wget >/dev/null 2>&1; then
        tmp_headers=$(mktemp)
        releases=$(wget -S -qO- \
            ${GITHUB_TOKEN:+--header "Authorization: Bearer ${GITHUB_TOKEN}"} \
            "${VUJA_API_URL}/repos/${REPO}/releases/latest" 2>"$tmp_headers" || true)
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

    archive_url=$(echo "${releases}" | grep "browser_download_url" | grep "${arch}" | head -1 | cut -d '"' -f 4)
    checksum_url=$(echo "${releases}" | grep "browser_download_url" | grep "SHA256SUMS" | head -1 | cut -d '"' -f 4)
    printf '%s\n%s\n' "${archive_url}" "${checksum_url}"
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
