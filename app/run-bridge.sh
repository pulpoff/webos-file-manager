#!/bin/sh
# Keeps the file service (bin/fmbridge) alive: restarts it 2 s after it exits or is killed.
# Stops when the app has been gone for 2 minutes (an update reinstalls it within that time).
APP=/media/developer/apps/usr/palm/applications/com.pulp.smbbrowser
ROOT=/media/developer/smb-browser
LOG=$ROOT/server.log
echo -500 > /proc/$$/oom_score_adj 2>/dev/null
gone=0
while true; do
  if [ -f "$LOG" ] && [ "$(wc -c < "$LOG")" -gt 1048576 ]; then mv -f "$LOG" "$LOG.1"; fi
  if [ -f "$APP/bin/fmbridge" ]; then
    gone=0
    chmod 755 "$APP/bin/fmbridge" 2>/dev/null
    echo "$(date) starting fmbridge" >>"$LOG"
    "$APP/bin/fmbridge" >>"$LOG" 2>&1 </dev/null
    echo "$(date) fmbridge exited with $?" >>"$LOG"
  else
    gone=$((gone + 1))
    [ "$gone" -ge 60 ] && exit 0
  fi
  sleep 2
done
