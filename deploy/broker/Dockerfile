# Both stages pin the multi-architecture Alpine index digest.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS build
RUN apk add --no-cache build-base=0.5-r4 cmake=4.2.3-r0 openssl-dev=3.5.9-r0 cjson-dev=1.7.19-r1 curl=8.22.0-r0
WORKDIR /src
RUN curl -fsSL https://mosquitto.org/files/source/mosquitto-2.1.2.tar.gz -o source.tar.gz \
    && echo 'fd905380691ac65ea5a93779e8214941829e3d6e038d5edff9eac5fd74cbed02  source.tar.gz' | sha256sum -c - \
    && tar -xzf source.tar.gz \
    && cmake -S mosquitto-2.1.2 -B build -DWITH_TLS=ON -DWITH_WEBSOCKETS=OFF -DWITH_DOCS=OFF -DWITH_PLUGINS=ON -DWITH_PLUGIN_DYNAMIC_SECURITY=ON -DWITH_PLUGIN_ACL_FILE=OFF -DWITH_PLUGIN_PASSWORD_FILE=OFF -DWITH_PLUGIN_PERSIST_SQLITE=OFF -DWITH_PLUGIN_SPARKPLUG_AWARE=OFF -DWITH_PLUGIN_EXAMPLES=OFF -DWITH_TESTS=OFF -DCMAKE_INSTALL_PREFIX=/opt/install \
    && cmake --build build -j 2 && cmake --install build
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache libssl3=3.5.9-r0 libcrypto3=3.5.9-r0 cjson=1.7.19-r1 \
    && addgroup -g 1883 mosquitto && adduser -D -H -u 1883 -G mosquitto mosquitto \
    && mkdir -p /mosquitto/data && chown 1883:1883 /mosquitto/data
COPY --from=build /opt/install/sbin/mosquitto /usr/local/bin/mosquitto
COPY --from=build /opt/install/bin/mosquitto_pub /opt/install/bin/mosquitto_sub /usr/local/bin/
COPY --from=build /opt/install/lib/ /usr/local/lib/
USER 1883:1883
EXPOSE 1883
ENTRYPOINT ["mosquitto"]
CMD ["-c", "/mosquitto/config/mosquitto.conf"]
