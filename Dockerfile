# syntax=docker/dockerfile:1
# Production image: one static binary on distroless. Generated code (sqlc,
# templ) is committed, so the build needs only the Go toolchain.
# Build with `make image` (linux/amd64, tagged with the git SHA).

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/Zagoman/meal-planner" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.title="meal-planner"
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENV ADDR=:8080
# No HEALTHCHECK here: compose runs `/server healthcheck` (distroless has no shell).
ENTRYPOINT ["/server"]
