#!/usr/bin/env bash
# Local build of the Android app (same steps as .github/workflows/android.yml).
# Needs: Go 1.24+, JDK 17, Android SDK + NDK (ANDROID_NDK_HOME set), gradle 8.9+, git.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$PWD"
: "${ANDROID_NDK_HOME:?set ANDROID_NDK_HOME to your NDK folder}"

echo "== dnstt-client (arm64, static) =="
rm -rf /tmp/dnstt
git clone --depth 1 https://www.bamsoftware.com/git/dnstt.git /tmp/dnstt
mkdir -p android/app/src/main/jniLibs/arm64-v8a
(cd /tmp/dnstt/dnstt-client && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w" \
  -o "$ROOT/android/app/src/main/jniLibs/arm64-v8a/libdnstt.so" .)

echo "== Go library =="
go get golang.org/x/mobile/bind
go mod tidy
go test -count=1 .
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
"$(go env GOPATH)/bin/gomobile" init
mkdir -p android/app/libs
"$(go env GOPATH)/bin/gomobile" bind -target=android/arm64 -androidapi 24 \
  -o android/app/libs/multislow.aar ./mobile

echo "== APK =="
(cd android && gradle --no-daemon assembleDebug)
echo "Done: android/app/build/outputs/apk/debug/app-debug.apk"
