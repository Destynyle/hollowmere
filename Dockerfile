# Build stage -----------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first, so code changes do not refetch modules.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary: the runtime image has no libc.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/hollowmere ./cmd/hollowmere \
 && go build -trimpath -ldflags="-s -w" -o /out/tapcli ./cmd/tapcli

# Runtime stage ---------------------------------------------------------
FROM alpine:3.21
RUN apk add --no-cache wget ca-certificates \
 && adduser -D -u 10001 hollow
WORKDIR /app
COPY --from=build /out/hollowmere /out/tapcli /usr/local/bin/
COPY data/ /app/data/

USER hollow
EXPOSE 8080 4243
ENV TAP_HTTP=0.0.0.0:8080 \
    TAP_TCP=0.0.0.0:4243 \
    TAP_WORLD=/app/data/world.json

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["hollowmere"]
