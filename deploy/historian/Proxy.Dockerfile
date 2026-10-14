# Build artifacts/hades-history-proxy for your Linux target before Compose build.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY artifacts/hades-history-proxy /usr/local/bin/hades-history-proxy
USER 10001:10001
ENTRYPOINT ["hades-history-proxy"]
