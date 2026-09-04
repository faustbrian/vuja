#!/bin/sh

purge=0
case "$#" in
    0) ;;
    1)
        if [ "$1" = "--purge" ]; then
            purge=1
        else
            echo "Error: unknown option: $1 (supported: --purge)" >&2
            exit 1
        fi
        ;;
    *)
        echo "Error: uninstall accepts only the optional --purge flag" >&2
        exit 1
        ;;
esac

echo "Uninstalling Vuja..."

failed=0

remove_file() {
    target="$1"
    if [ ! -e "${target}" ] && [ ! -L "${target}" ]; then
        return 0
    fi
    if /bin/rm -f "${target}" 2>/dev/null || rm -f "${target}" 2>/dev/null; then
        return 0
    fi
    echo "Error: could not remove ${target}" >&2
    failed=1
    return 1
}

remove_tree() {
    target="$1"
    if [ ! -e "${target}" ] && [ ! -L "${target}" ]; then
        return 0
    fi
    if /bin/rm -rf "${target}" 2>/dev/null || rm -rf "${target}" 2>/dev/null; then
        return 0
    fi
    echo "Error: could not remove ${target}" >&2
    failed=1
    return 1
}

for loc in "${HOME}/.local/bin/vuja" "/usr/local/bin/vuja"; do
    if [ -f "${loc}" ]; then
        echo "Removing binary: ${loc}"
        if [ -w "$(dirname "${loc}")" ]; then
            remove_file "${loc}" || true
        else
            if ! sudo /bin/rm -f "${loc}" 2>/dev/null && ! sudo rm -f "${loc}" 2>/dev/null; then
                echo "Error: could not remove ${loc}" >&2
                failed=1
            fi
        fi
    fi
done

for file in "${HOME}/.zshrc" "${HOME}/.bashrc" "${HOME}/.config/fish/config.fish"; do
    if [ -f "${file}" ]; then
        echo "Removing integration from ${file}..."
        if ! tmp_file=$(mktemp); then
            echo "Error: could not create a temporary file for ${file}" >&2
            failed=1
            continue
        fi
        grep -v -i -E "(# vuja autocomplete|# vuja autostart|vuja init|source .*/vuja/init\\.)" "${file}" > "${tmp_file}" 2>/dev/null
        grep_exit_code=$?
        if [ "${grep_exit_code}" -eq 0 ] || [ "${grep_exit_code}" -eq 1 ]; then
            if ! mv "${tmp_file}" "${file}"; then
                echo "Error: could not update ${file}" >&2
                failed=1
                remove_file "${tmp_file}" || true
            fi
        else
            echo "Error: could not inspect ${file}" >&2
            failed=1
            remove_file "${tmp_file}" || true
        fi
    fi
done

if [ -n "${XDG_CONFIG_HOME}" ]; then
    config_home="${XDG_CONFIG_HOME}"
elif [ "$(uname -s)" = "Darwin" ]; then
    config_home="${HOME}/Library/Application Support"
else
    config_home="${HOME}/.config"
fi
data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"
if [ -n "${XDG_CACHE_HOME}" ]; then
    cache_home="${XDG_CACHE_HOME}"
elif [ "$(uname -s)" = "Darwin" ]; then
    cache_home="${HOME}/Library/Caches"
else
    cache_home="${HOME}/.cache"
fi

remove_tree "${HOME}/Applications/Vuja URL Handler.app" || true
remove_file "${HOME}/.local/share/applications/vuja-url-handler.desktop" || true
remove_file "${data_home}/applications/vuja-url-handler.desktop" || true

cache_removed=0
if remove_tree "${cache_home}/vuja"; then
    cache_removed=1
fi
if [ "${purge}" -eq 1 ]; then
    config_removed=0
    state_removed=0
    if remove_tree "${config_home}/vuja"; then config_removed=1; fi
    if remove_tree "${data_home}/vuja"; then state_removed=1; fi
    if [ "${cache_removed}" -eq 1 ] && [ "${config_removed}" -eq 1 ] && [ "${state_removed}" -eq 1 ]; then
        echo "✓ Removed configuration, durable history, state, and cache data"
    fi
else
    if [ "${cache_removed}" -eq 1 ]; then
        echo "✓ Removed disposable cache data"
    fi
    echo "✓ Preserved configuration and durable history; use --purge to remove them"
fi
remove_file "vuja.log" || true

if [ "${failed}" -ne 0 ]; then
    echo "Error: Vuja uninstall did not complete; review the errors above" >&2
    exit 1
fi

echo "✓ Vuja has been successfully uninstalled"
if [ -n "${VUJA_PID}" ]; then
    echo ""
    echo "⚠️  You are currently inside an active Vuja session."
    echo "Vuja runs as the parent process of this terminal - do NOT run 'pkill vuja'"
    echo "as it will immediately close this terminal window."
    echo ""
    echo "To fully exit, simply close this terminal window and open a new one."
    echo "Vuja will not start again since the shell config has been cleaned up."
else
    echo "Please close and reopen your terminal to complete the uninstall."
fi
