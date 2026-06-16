# --- build stage ---
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/rosterbot .

# --- runtime stage ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium ca-certificates awscli tini \
 && rm -rf /var/lib/apt/lists/*
ENV CHROME_BIN=/usr/bin/chromium
WORKDIR /app
COPY --from=build /out/rosterbot /app/rosterbot
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
