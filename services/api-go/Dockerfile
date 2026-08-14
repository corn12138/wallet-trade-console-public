# syntax=docker/dockerfile:1.7

FROM golang:1.25.13-alpine AS builder
WORKDIR /src/services/api-go
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# Cache modules separately from source so dep-only changes hit the build cache.
COPY services/api-go/go.mod services/api-go/go.sum* ./
RUN go mod download

COPY services/api-go/ ./
# Strict zero-Nest: one image, five production roles. Build all five Go binaries
# so the same image can run as the REST+realtime gateway (cmd/api, default), the
# indexer (cmd/indexer), the standalone market-stream worker (cmd/market-stream),
# the real-price ingest worker (cmd/pricefeed), or the cross-chain bridge
# relayer (cmd/bridgerelayer).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/indexer ./cmd/indexer && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/market-stream ./cmd/market-stream && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pricefeed ./cmd/pricefeed && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bridgerelayer ./cmd/bridgerelayer

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/indexer /app/indexer
COPY --from=builder /out/market-stream /app/market-stream
COPY --from=builder /out/pricefeed /app/pricefeed
COPY --from=builder /out/bridgerelayer /app/bridgerelayer
COPY packages/shared/deployments /app/packages/shared/deployments
ENV CONTRACTS_DEPLOYMENTS_DIR=/app/packages/shared/deployments
EXPOSE 8080
USER nonroot:nonroot
# Default role = REST + realtime gateway (backward-compatible with wallet-trade-api-go).
# Other roles override the entrypoint in compose, e.g.:
#   entrypoint: ["/app/indexer"]        # wallet-trade-indexer-go
#   entrypoint: ["/app/market-stream"]  # wallet-trade-market-stream-go
#   entrypoint: ["/app/pricefeed"]      # wallet-trade-pricefeed-go
#   entrypoint: ["/app/bridgerelayer"]  # wallet-trade-bridgerelayer-go
ENTRYPOINT ["/app/api"]
