#!/bin/sh
# Builds the bridge for the TV (static linux/arm64) and packages output/<id>_<version>_all.ipk
# in the same layout ares-package produces, plus output/<id>.manifest.json for the Homebrew
# Channel. Needs Go and Python 3; no webOS SDK required.
set -e
cd "$(dirname "$0")"
ID=$(sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' app/appinfo.json)
VER=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' app/appinfo.json)
(cd bridge && sed -i "s/^const version = .*/const version = \"$VER\"/" main.go && \
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../app/bin/fmbridge .)
sed -i "s/^var APP_VERSION = '[^']*'/var APP_VERSION = '$VER'/" app/js/app.js
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
D=$T/data/usr/palm/applications/$ID
mkdir -p "$D" "$T/data/usr/palm/packages/$ID" "$T/control"
cp -r app/. "$D/"
chmod 755 "$D/bin/fmbridge" "$D/run-bridge.sh" "$D/98-smbbrowser" "$D/setup.sh" "$D/get-ffmpeg.sh"
printf '{\n  "id": "%s",\n  "version": "%s",\n  "app": "%s"\n}\n' "$ID" "$VER" "$ID" > "$T/data/usr/palm/packages/$ID/packageinfo.json"
SIZE=$(du -sb "$T/data" | cut -f1)
cat > "$T/control/control" <<EOF
Package: $ID
Version: $VER
Section: misc
Priority: optional
Architecture: all
Installed-Size: $SIZE
Maintainer: N/A <nobody@example.com>
Description: This is a webOS application.
webOS-Package-Format-Version: 2
webOS-Packager-Version: x.y.x
EOF
printf '2.0\n' > "$T/debian-binary"
(cd "$T/control" && tar --owner=0 --group=0 -czf ../control.tar.gz control)
(cd "$T/data" && tar --owner=0 --group=0 -czf ../data.tar.gz usr)
mkdir -p output
OUT=output/${ID}_${VER}_all.ipk
rm -f "$OUT"
# webOS's verifier rejects GNU ar output ("name/" members, zero mtime), so write
# the ar container the way ares-package does: plain names, real mtime, mode 100644.
python3 - "$OUT" "$T/debian-binary" "$T/control.tar.gz" "$T/data.tar.gz" <<'PY2'
import os, sys, time
out = open(sys.argv[1], 'wb'); out.write(b'!<arch>\n')
for p in sys.argv[2:]:
    data = open(p, 'rb').read()
    hdr = '%-16s%-12d%-6d%-6d%-8s%-10d`\n' % (os.path.basename(p), int(time.time()), 0, 0, '100644', len(data))
    out.write(hdr.encode()); out.write(data)
    if len(data) % 2: out.write(b'\n')
out.close()
PY2
# The Homebrew Channel manifest; the release publishes it next to the IPK.
python3 - "$OUT" app/appinfo.json "output/$ID.manifest.json" <<'PY3'
import hashlib, json, os, sys
ipk, info, out = sys.argv[1:]
a = json.load(open(info))
m = {
    "id": a["id"], "version": a["version"], "type": a["type"], "title": a["title"],
    "appDescription": "Browse USB drives, SMB shares and SFTP servers, and play video, music and photos",
    "iconUri": "https://raw.githubusercontent.com/pulpoff/webos-file-manager/main/app/fm-largeIcon.png",
    "sourceUrl": "https://github.com/pulpoff/webos-file-manager",
    "rootRequired": True,
    "ipkUrl": os.path.basename(ipk),
    "ipkHash": {"sha256": hashlib.sha256(open(ipk, "rb").read()).hexdigest()},
}
json.dump(m, open(out, "w"), indent=2); open(out, "a").write("\n")
PY3
ls -la "$OUT" "output/$ID.manifest.json"
