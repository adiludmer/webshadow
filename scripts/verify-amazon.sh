#!/usr/bin/env bash
# Records the scripted Amazon session in a headed Chromium under Xvfb and
# checks the recording and its clustering. The recording stays local; only
# check results and screenshots are printed or written.
#
#   scripts/verify-amazon.sh [chromium] [screenshot dir]
#
# Needs Xvfb, python3 with python-xlib, and Go. AMAZON_TERMS records a
# longer session instead (see scripts/amazon-session.py).
set -euo pipefail
cd "$(dirname "$0")/.."
chromium=${1:-/opt/pw-browsers/chromium}
shots=${2:-}
work=$(mktemp -d)
trap 'kill "$browser" "$xvfb" 2>/dev/null || true; rm -rf "$work"' EXIT

go build -o "$work/webshadow" ./cmd/webshadow
display=:${WEBSHADOW_DISPLAY:-99}
Xvfb "$display" -screen 0 1366x900x24 >/dev/null 2>&1 &
xvfb=$!
sleep 1
DISPLAY=$display "$work/webshadow" browser -chromium "$chromium" https://www.amazon.com/ >"$work/browser.log" 2>&1 &
browser=$!
DISPLAY=$display python3 -I scripts/amazon-session.py $shots
kill -INT "$browser"
wait "$browser" || true
rec=$(sed -n 's/^Recording: //p' "$work/browser.log" | head -1)
if [ -z "$rec" ]; then
	cat "$work/browser.log" >&2
	exit 1
fi
echo "recording: $(basename "$rec")"
go run ./scripts/verify-amazon "$rec"
