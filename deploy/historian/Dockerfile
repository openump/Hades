FROM victoriametrics/victoria-metrics:v1.154.0@sha256:4bd592341587d669b7f57d54bbb82bd21ac8675f4e24234b7703a7c0f85197d8 AS upstream
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --from=upstream /victoria-metrics-prod /usr/local/bin/victoria-metrics
RUN addgroup -g 10001 historian && adduser -D -H -u 10001 -G historian historian && mkdir /storage && chown historian:historian /storage
USER 10001:10001
ENTRYPOINT ["victoria-metrics"]
