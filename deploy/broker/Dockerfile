# syntax=docker/dockerfile:1.10
# Both stages pin the multi-architecture Alpine index digest. The Alpine branch
# repository keeps only the newest revision of each package, so APK pins use
# "~major.minor": security revisions apply, version jumps need review here.
# The Mosquitto source archive is verified by checksum.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS build
RUN apk add --no-cache --upgrade build-base~0.5 cmake~4.2 openssl-dev~3.5 cjson-dev~1.7
WORKDIR /src
ADD --checksum=sha256:fd905380691ac65ea5a93779e8214941829e3d6e038d5edff9eac5fd74cbed02 \
    https://mosquitto.org/files/source/mosquitto-2.1.2.tar.gz source.tar.gz
RUN tar -xzf source.tar.gz \
    && cmake -S mosquitto-2.1.2 -B build -DWITH_TLS=ON -DWITH_WEBSOCKETS=OFF -DWITH_DOCS=OFF -DWITH_PLUGINS=ON -DWITH_PLUGIN_DYNAMIC_SECURITY=ON -DWITH_PLUGIN_ACL_FILE=OFF -DWITH_PLUGIN_PASSWORD_FILE=OFF -DWITH_PLUGIN_PERSIST_SQLITE=OFF -DWITH_PLUGIN_SPARKPLUG_AWARE=OFF -DWITH_PLUGIN_EXAMPLES=OFF -DWITH_TESTS=OFF -DCMAKE_INSTALL_PREFIX=/opt/install \
    && cmake --build build -j 2 && cmake --install build
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache --upgrade libssl3~3.5 libcrypto3~3.5 cjson~1.7 \
    && addgroup -g 1883 mosquitto && adduser -D -H -u 1883 -G mosquitto mosquitto \
    && mkdir -p /mosquitto/data && chown 1883:1883 /mosquitto/data
COPY --from=build /opt/install/sbin/mosquitto /usr/local/bin/mosquitto
COPY --from=build /opt/install/bin/mosquitto_pub /opt/install/bin/mosquitto_sub /usr/local/bin/
COPY --from=build /opt/install/lib/ /usr/local/lib/
USER 1883:1883
EXPOSE 1883
ENTRYPOINT ["mosquitto"]
CMD ["-c", "/mosquitto/config/mosquitto.conf"]
