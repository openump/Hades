# syntax=docker/dockerfile:1.10
# One multicall `hades` binary for every role (Hades-041). Both stages pin the
# multi-architecture index digest; the build is CGO-free and reproducible
# (-trimpath, no VCS stamping), and the image runs as an unprivileged user.
FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/hades ./cmd/hades \
    && for role in $(/out/hades roles); do ln -s hades "/out/$role"; done

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --from=build /out/ /usr/local/bin/
# Contracts and the reviewed example catalog the roles validate against.
COPY schemas /opt/hades/schemas
COPY examples/site /opt/hades/examples/site
COPY config/normalization /opt/hades/config/normalization
# A fresh named volume inherits this ownership, so the unprivileged roles can
# write their WAL and quarantine without running as root.
RUN mkdir -p /var/lib/hades && chown 10001:10001 /var/lib/hades && chmod 0700 /var/lib/hades
WORKDIR /opt/hades
USER 10001:10001
ENTRYPOINT ["hades"]
