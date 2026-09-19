# syntax=docker/dockerfile:1

# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it sonarr-mcp sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24

FROM golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /sonarr-mcp .

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 65532 sonarrmcp
COPY --from=build /sonarr-mcp /usr/local/bin/sonarr-mcp
USER sonarrmcp
# HTTP transport by default in the container; set SONARR_SERVER and SONARR_TOKEN at runtime,
# SONARR_AUTH_TOKEN to protect the endpoint. the healthcheck assumes the default port.
ENV SONARR_LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["sonarr-mcp"]
CMD ["serve"]
