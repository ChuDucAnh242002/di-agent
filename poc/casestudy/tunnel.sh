#!/usr/bin/env bash
# tunnel.sh — carry the host's Ollama to the node: the node's localhost:11435 is the
# host's localhost:11434, for as long as this runs. Nothing else is exposed; Ollama on the
# host keeps listening on 127.0.0.1 only. Runs on the host (macOS); reconnects if the link
# drops.
#
# Usage: ./tunnel.sh [vm-name]               run in the foreground (default di-node)
#        ./tunnel.sh --install [vm-name]     run as a launchd agent: starts at login, restarted
#                                            if it exits, independent of any terminal or session
#        ./tunnel.sh --uninstall [vm-name]   remove that agent
# The key is provision.sh's ~/.ssh/<vm>-tunnel.
set -uo pipefail
MODE=run
case "${1:-}" in --install) MODE=install; shift ;; --uninstall) MODE=uninstall; shift ;; esac
VM="${1:-di-node}"
KEY="$HOME/.ssh/$VM-tunnel"
LABEL="fi.tuni.di-agent.$VM-tunnel"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"

case "$MODE" in
install)
    # launchd runs a copy, so the job does not depend on where this checkout lives.
    HOME_DIR="$HOME/Library/Application Support/di-agent"
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs" "$HOME_DIR"
    cp "$SELF" "$HOME_DIR/tunnel.sh"
    SELF="$HOME_DIR/tunnel.sh"
    cat >"$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>$SELF</string><string>$VM</string></array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/$LABEL.log</string>
</dict></plist>
PLIST
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    echo "[tunnel] installed $LABEL; log $HOME/Library/Logs/$LABEL.log"
    exit 0 ;;
uninstall)
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    rm -f "$PLIST"
    echo "[tunnel] removed $LABEL"
    exit 0 ;;
esac

[ -f "$KEY" ] || { echo "no key $KEY: run provision.sh" >&2; exit 2; }
while :; do
    IP=$(multipass info "$VM" 2>/dev/null | awk '/IPv4/ {print $2; exit}')
    if [ -n "$IP" ]; then
        ssh -i "$KEY" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ExitOnForwardFailure=yes \
            -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -N -R 11435:127.0.0.1:11434 "ubuntu@$IP"
        echo "[tunnel] $(date +%T) dropped ($?); reconnecting in 5 s" >&2
    else
        echo "[tunnel] $(date +%T) $VM has no IP (stopped?); retrying in 5 s" >&2
    fi
    sleep 5
done
