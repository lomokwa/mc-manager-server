# syntax=docker/dockerfile:1.7
#
# Builds the API only. The Minecraft JVM now runs in its own container (see
# Dockerfile.minecraft) so this image no longer needs a JDK at all — that was
# the single biggest chunk of build time before (downloading and extracting
# the full Oracle JDK on every build).

# ---- Build stage: full Go toolchain ----
FROM golang:1.25-bookworm AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go generate ./... && CGO_ENABLED=1 go build -o /out/server .

# ---- Dev stage: hot reload via air, used only by docker-compose.dev.yml ----
FROM build AS dev
RUN --mount=type=cache,target=/go/pkg/mod \
    go install github.com/air-verse/air@latest
CMD ["air"]

# ---- Runtime stage: prod. No Go toolchain, no Java, just the binary. ----
FROM debian:bookworm-slim AS runtime
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/server ./server

EXPOSE 8080
CMD ["./server"]
