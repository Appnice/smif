#!/bin/bash
# Copy the recordings into site/media: web-ready mp4 (faststart) + webm, and posters.
# Run from site/demo after `vhs tapes/hero.tape` and `vhs tapes/chat.tape`.
set -euo pipefail
cd "$(dirname "$0")"
M=../media
mkdir -p "$M"
for n in hero chat; do
  ffmpeg -loglevel error -y -i out/$n.mp4 -c copy -movflags +faststart "$M/$n.mp4"
  cp out/$n.webm "$M/$n.webm"
done
# Posters: a frame with the list drawn.
ffmpeg -loglevel error -y -ss 2.4 -i out/hero.mp4 -frames:v 1 -q:v 3 "$M/hero.jpg"
ffmpeg -loglevel error -y -ss 4.5 -i out/chat.mp4 -frames:v 1 -q:v 3 "$M/chat.jpg"
ls -la "$M"
