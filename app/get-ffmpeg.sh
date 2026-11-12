#!/bin/sh
# Downloads John Van Sickle's static arm64 build of ffmpeg (GPLv3, https://johnvansickle.com/ffmpeg/)
# into the file service's tools directory, then restarts the service so it finds it.
# The app runs this as root through the Homebrew Channel when you agree to the download.
URL=https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-arm64-static.tar.xz
ROOT=/media/developer/smb-browser
BIN=$ROOT/bin
T=$ROOT/ffmpeg-download
fail() { echo "$*" >&2; rm -rf "$T"; exit 1; }
[ "$(id -u)" = 0 ] || fail "This needs root."
# The archive (19 MB) unpacks to about 110 MB next to it; ffmpeg and ffprobe keep about 100 MB.
free=$(df -Pk "$ROOT" 2>/dev/null | awk 'NR==2 {print $4}')
[ "${free:-0}" -ge 160000 ] || fail "Not enough free space: the download needs about 160 MB."
rm -rf "$T"; mkdir -p "$T" "$BIN" || fail "Cannot create $T."
curl -fsSL --retry 2 --connect-timeout 20 -o "$T/ffmpeg.tar.xz" "$URL" || fail "The download failed."
want=$(curl -fsSL --connect-timeout 20 "$URL.md5" | cut -d' ' -f1)
got=$(md5sum "$T/ffmpeg.tar.xz" | cut -d' ' -f1)
[ -n "$want" ] && [ "$want" = "$got" ] || fail "The download is damaged (its checksum does not match)."
xzcat "$T/ffmpeg.tar.xz" | tar -x -C "$T" || fail "Unpacking failed."
D=$(ls -d "$T"/ffmpeg-*-static 2>/dev/null | head -1)
[ -f "$D/ffmpeg" ] && [ -f "$D/ffprobe" ] || fail "The archive does not contain ffmpeg."
for f in ffmpeg ffprobe GPLv3.txt readme.txt; do
  [ -f "$D/$f" ] && mv -f "$D/$f" "$BIN/$f"
done
chmod 755 "$BIN/ffmpeg" "$BIN/ffprobe"
rm -rf "$T"
"$BIN/ffmpeg" -version | head -1
pkill -x fmbridge
exit 0
