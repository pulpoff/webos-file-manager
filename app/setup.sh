#!/bin/sh
# Root setup for File Manager. The app runs this as root through the Homebrew Channel's exec
# method on first launch, and again after an update. It installs the boot hook and starts
# (or restarts) the file service.
APP=/media/developer/apps/usr/palm/applications/com.pulp.smbbrowser
ROOT=/media/developer/smb-browser
HOOK=/var/lib/webosbrew/init.d/98-smbbrowser
if [ "$(id -u)" != 0 ]; then
  echo "The Homebrew Channel is not running as root, so the file service cannot be started. File Manager needs a rooted TV." >&2
  exit 2
fi
mkdir -p "$ROOT" /var/lib/webosbrew/init.d || exit 1
cp -f "$APP/98-smbbrowser" "$HOOK.new" && chmod 755 "$HOOK.new" && mv -f "$HOOK.new" "$HOOK" || exit 1
# A changed supervisor script means restarting the whole chain; otherwise just the service.
cmp -s "$APP/run-bridge.sh" "$ROOT/run-bridge.sh" || pkill -f "[r]un-bridge.sh"
pkill -x fmbridge
"$HOOK"
echo "File service started"
