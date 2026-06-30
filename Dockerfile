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
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium ca-certificates tini curl \
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
