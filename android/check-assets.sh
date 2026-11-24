#!/usr/bin/env bash
# Fails if the app would open to an error because web interface files are missing.
#   check-assets.sh source          checks android/app/src/main/assets (run after copying ../static into it)
#   check-assets.sh apk FILE.apk    checks the built APK itself
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
mode="${1:-}"
case "$mode" in
  source) prefix=""; list="$(cd "$here/app/src/main/assets" && find . -type f | sed 's#^\./##')" ;;
  apk) prefix="assets/"; list="$(unzip -Z1 "${2:?usage: check-assets.sh apk FILE.apk}")" ;;
  *) echo "usage: check-assets.sh source | apk FILE.apk"; exit 2 ;;
esac
fail=0
need() { if ! grep -qxF "$prefix$1" <<<"$list"; then echo "MISSING from the app: $prefix$1"; fail=1; fi; }

for f in app.html app.js chat.js ui.js brand.js push.js native.js ux.js style.css ui.css app.css logo-mark.svg; do need "static/$f"; done
need "setup.html"
if [ "$mode" = apk ]; then
  for f in classes.dex AndroidManifest.xml; do grep -qxF "$f" <<<"$list" || { echo "MISSING from the APK: $f"; fail=1; }; done
fi
# Every file the two pages load must be there too.
refs="$(grep -ohE '(src|href)="/static/[^"?#]+' "$here/../static/app.html" "$here/app/src/main/assets/setup.html" | sed -E 's#^(src|href)="/static/##' | sort -u)"
for r in $refs; do need "static/$r"; done
if [ "$fail" -ne 0 ]; then echo "The web interface is not fully bundled. Copy ../static/. into android/app/src/main/assets/static/ before building."; exit 1; fi
echo "OK: the app contains its interface ($(wc -w <<<"$refs") referenced files checked)"
