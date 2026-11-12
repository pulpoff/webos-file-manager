#!/bin/sh
# Downloads John Van Sickle's static arm64 build of ffmpeg (GPLv3, https://johnvansickle.com/ffmpeg/)
# into the file service's tools directory, then restarts the service so it finds it. Run as root.
#   get-ffmpeg.sh          download in the foreground
#   get-ffmpeg.sh start    download in the background and return at once (the app does this
#                          through the Homebrew Channel when you agree to the download)
#   get-ffmpeg.sh status   print "phase bytes total message" for the app's progress bar; the
#                          phases are start, download, check, unpack, restart, done and error
URL=https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-arm64-static.tar.xz
ROOT=/media/developer/smb-browser
BIN=$ROOT/bin
T=$ROOT/ffmpeg-download
S=$ROOT/ffmpeg-download.state

state() { echo "$*" > "$S"; }
fail() { state "error 0 $*"; echo "$*" >&2; rm -rf "$T"; exit 1; }

case "$1" in
start)
	[ "$(id -u)" = 0 ] || { echo "This needs root." >&2; exit 1; }
	pgrep -f "[g]et-ffmpeg.sh run" >/dev/null && exit 0
	state "start 0"
	nohup /bin/sh "$0" run >/dev/null 2>&1 </dev/null &
	exit 0
	;;
status)
	set -- $(cat "$S" 2>/dev/null)
	phase=${1:-none} total=${2:-0}
	[ $# -ge 2 ] && shift 2
	bytes=0
	[ -f "$T/ffmpeg.tar.xz" ] && bytes=$(wc -c < "$T/ffmpeg.tar.xz")
	echo "$phase $bytes $total $*"
	exit 0
	;;
esac

[ "$(id -u)" = 0 ] || fail "This needs root."
# The archive (19 MB) unpacks to about 110 MB next to it; ffmpeg and ffprobe keep about 100 MB.
free=$(df -Pk "$ROOT" 2>/dev/null | awk 'NR==2 {print $4}')
[ "${free:-0}" -ge 160000 ] || fail "Not enough free space: the download needs about 160 MB."
rm -rf "$T"; mkdir -p "$T" "$BIN" || fail "Cannot create $T."
total=$(curl -fsSIL --connect-timeout 20 "$URL" | tr -d '\r' | awk 'tolower($1) == "content-length:" {n = $2} END {print n + 0}')
state "download $total"
curl -fsSL --retry 2 --connect-timeout 20 -o "$T/ffmpeg.tar.xz" "$URL" || fail "The download failed."
state "check $total"
want=$(curl -fsSL --connect-timeout 20 "$URL.md5" | cut -d' ' -f1)
got=$(md5sum "$T/ffmpeg.tar.xz" | cut -d' ' -f1)
[ -n "$want" ] && [ "$want" = "$got" ] || fail "The download is damaged (its checksum does not match)."
state "unpack $total"
xzcat "$T/ffmpeg.tar.xz" | tar -x -C "$T" || fail "Unpacking failed."
D=$(ls -d "$T"/ffmpeg-*-static 2>/dev/null | head -1)
[ -f "$D/ffmpeg" ] && [ -f "$D/ffprobe" ] || fail "The archive does not contain ffmpeg."
for f in ffmpeg ffprobe GPLv3.txt readme.txt; do
	[ -f "$D/$f" ] && mv -f "$D/$f" "$BIN/$f"
done
chmod 755 "$BIN/ffmpeg" "$BIN/ffprobe"
rm -rf "$T"
state "restart $total"
"$BIN/ffmpeg" -version | head -1
pkill -x fmbridge
state "done $total"
exit 0
