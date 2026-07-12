# --- build stage ---
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -s -w strip the symbol table and DWARF debug info; a production container never
# debugs the binary in place, and it trims the executable by ~25%.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/rosterbot .

# --- runtime stage ---
FROM debian:bookworm-slim
# awscli is gone: the S3 sync + CloudFront invalidation it performed now runs in
# the bot itself (internal/statesync), dropping ~120MB of python+awscli.
#
# chromium depends (transitively, via libgl1->mesa) on libLLVM-15.so, a 109MB
# JIT that backs mesa's llvmpipe software-GL driver. apt can't drop the mesa
# package without removing chromium, but the Fantrax chromedp login runs with
# --disable-gpu and never initialises system GL, so the shared object is never
# loaded. We delete just that .so and then prove headless chromium still renders
# DOM without it — the `grep -q` fails the build loudly if the assumption breaks.
#
# CHROMIUM_VERSION is pinned (not just "chromium" latest): on 2026-07-06
# debian-security bumped chromium to 150.0.7871.46-1~deb12u1 mid-cycle and it
# started crashing (SIGTRAP) on the CodeBuild Graviton (arm64) build host during
# this exact headless smoke test — 149.0.7827.196-1~deb12u1 had built clean days
# earlier with no Dockerfile change. Debian doesn't keep old point versions
# around once superseded, so once broken there's no unpinned way back.
#
# 2026-07-12: the 147.0.7727.137-1~deb12u1 fallback pinned during that incident
# was itself dropped from the live bookworm/main repo (apt: "Version not found",
# exit 100) and broke every build for ~5h. The only version now available in
# plain bookworm/main is 150.0.7871.100-1~deb12u1 — a newer point release than
# the .46 that SIGTRAP'd, so the arm64 crash may be fixed. It is validated by the
# CI build host itself via the headless smoke test below: if .100 still crashes
# on Graviton, the build fails loudly here (this smoke test can pass locally on
# Apple Silicon while crashing on Graviton, which is exactly what bit us before).
# If it fails, the next currently-available bookworm/main version is the only
# way forward — snapshot.debian.org still carries the older validated binaries if
# a permanent pin to a known-good version is ever preferred over chasing current.
ARG CHROMIUM_VERSION=150.0.7871.100-1~deb12u1
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium=${CHROMIUM_VERSION} chromium-common=${CHROMIUM_VERSION} chromium-sandbox=${CHROMIUM_VERSION} \
      ca-certificates tini curl \
 && apt-mark hold chromium chromium-common chromium-sandbox \
 && rm -rf /var/lib/apt/lists/* \
 && rm -f /usr/lib/*/libLLVM-15.so* \
 && chromium --headless=new --disable-gpu --no-sandbox --dump-dom \
      'data:text/html,<h1>render-ok</h1>' | grep -q render-ok
ENV CHROME_BIN=/usr/bin/chromium
WORKDIR /app
COPY --from=build /out/rosterbot /app/rosterbot
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
