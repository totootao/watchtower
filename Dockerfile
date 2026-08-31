# syntax=docker/dockerfile:1
#
# Self-contained build for the Watchtower web-dashboard fork.
# Builds the binary from the local source context (no external git clone),
# so it works both locally and in GitHub Actions without uploading secrets.

ARG GO_VERSION=1.27
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

# Build dependencies only; the Tencent Go module proxy keeps CI offline-safe.
ENV GOPROXY=https://mirrors.tencent.com/go/,direct \
    GOTOOLCHAIN=local \
    CGO_ENABLED=0

# Alpine's default package CDN is unreachable from some build networks;
# point apk at the Tencent mirror instead. git is omitted because the build
# copies the source from the local context rather than cloning.
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.tencent.com#g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Map the Docker TARGETPLATFORM (e.g. linux/arm64/v8) to Go GOARCH/GOARM.
ARG TARGETPLATFORM=linux/amd64
ARG VERSION=dev
RUN export GOARCH="$(echo "${TARGETPLATFORM}" | cut -d/ -f2)" \
    && export GOARM="$(echo "${TARGETPLATFORM}" | cut -d/ -f3)" \
    && export GOARM_FLAG="${GOARM:+GOARM=${GOARM#v}}" \
    && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" ${GOARM_FLAG} \
       go build \
         -trimpath \
         -ldflags "-s -w -X github.com/nicholas-fedor/watchtower/internal/meta.Version=${VERSION}" \
         -o /out/watchtower \
         .

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /out/watchtower /watchtower

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["/watchtower", "--health-check"]

ENTRYPOINT ["/watchtower"]
