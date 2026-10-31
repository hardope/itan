# ---- build ----
# Runs on the build machine and cross-compiles for the target, so multi-arch
# images don't compile Go under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure Go SQLite driver: no cgo, fully static binary.
RUN if [ "$TARGETARCH" = arm ]; then export GOARM="${TARGETVARIANT#v}"; fi \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/itan ./cmd/itan

# ---- runtime ----
FROM alpine:3.24
# mesa-va-gallium / intel-media-driver: VAAPI drivers for hardware transcoding
# on AMD / Intel GPUs (used only if /dev/dri is passed through).
RUN apk add --no-cache ffmpeg tzdata mesa-va-gallium \
 && if [ "$(apk --print-arch)" = "x86_64" ]; then apk add --no-cache intel-media-driver; fi \
 && adduser -D -H -u 1000 itan \
 && mkdir -p /data /media \
 && chown itan /data
COPY --from=build /out/itan /usr/local/bin/itan

USER itan
ENV ITAN_ADDR=:8080 \
    ITAN_DATA=/data \
    ITAN_LIBRARIES=/media
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["itan"]
