#!/bin/sh

echo "Uninstalling Vuja..."

if command -v vuja >/dev/null 2>&1; then
    if vuja uninstall --help >/dev/null 2>&1; then
        vuja uninstall
        exit 0
    fi
fi

for loc in "${HOME}/.local/bin/vuja" "/usr/local/bin/vuja"; do
    if [ -f "${loc}" ]; then
        echo "Removing binary: ${loc}"
        if [ -w "$(dirname "${loc}")" ]; then
            /bin/rm -f "${loc}" 2>/dev/null || rm -f "${loc}"
        else
            sudo /bin/rm -f "${loc}" 2>/dev/null || sudo rm -f "${loc}"
        fi
    fi
done

for file in "${HOME}/.zshrc" "${HOME}/.bashrc" "${HOME}/.config/fish/config.fish"; do
    if [ -f "${file}" ]; then
        echo "Removing integration from ${file}..."
        tmp_file=$(mktemp)
        grep -v -i -E "(# vuja autocomplete|# vuja autostart|vuja init|source .*/vuja/init\\.)" "${file}" > "${tmp_file}" 2>/dev/null
        status=$?
        if [ "${status}" -eq 0 ] || [ "${status}" -eq 1 ]; then
            mv "${tmp_file}" "${file}"
        else
            /bin/rm -f "${tmp_file}" 2>/dev/null || rm -f "${tmp_file}"
        fi
    fi
done

/bin/rm -rf "${HOME}/.config/vuja" 2>/dev/null || rm -rf "${HOME}/.config/vuja"
/bin/rm -rf "${HOME}/.local/share/vuja" 2>/dev/null || rm -rf "${HOME}/.local/share/vuja"
/bin/rm -rf "${HOME}/.cache/vuja" 2>/dev/null || rm -rf "${HOME}/.cache/vuja"
/bin/rm -f "vuja.log" 2>/dev/null || rm -f "vuja.log"

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
