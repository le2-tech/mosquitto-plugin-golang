FROM debian:trixie-slim AS build-main

ENV DEBIAN_FRONTEND=noninteractive \
    VERSION=2.0.22 \
    DOWNLOAD_SHA256=2f752589ef7db40260b633fbdb536e9a04b446a315138d64a7ff3c14e2de6b68 \
    GPG_KEYS=A0D6EEA1DCAE49A635A3B2F0779B22DFB3E717B7

ARG APP_ENV=prod

RUN set -eux; \
  if [ "${APP_ENV:-}" = "dev" ]; then \
  sed -i "s|http://deb.debian.org|http://mirrors.aliyun.com|g" /etc/apt/sources.list.d/debian.sources; \
  fi

# 构建依赖（使用 Debian 组件提供的 libwebsockets-dev）
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates wget gnupg dirmngr pkg-config \
        build-essential \
        libcjson-dev libssl-dev uuid-dev \
        libwebsockets-dev binutils; \
    rm -rf /var/lib/apt/lists/*

# 下载 + 校验 Mosquitto 源码（sha256 + GPG）
RUN set -eux; \
    wget -O /tmp/mosq.tar.gz "https://mosquitto.org/files/source/mosquitto-${VERSION}.tar.gz"; \
    echo "${DOWNLOAD_SHA256}  /tmp/mosq.tar.gz" | sha256sum -c -; \
    wget -O /tmp/mosq.tar.gz.asc "https://mosquitto.org/files/source/mosquitto-${VERSION}.tar.gz.asc"; \
    export GNUPGHOME="$(mktemp -d)"; \
    found=''; \
    for server in hkps://keys.openpgp.org hkp://keyserver.ubuntu.com:80 pgp.mit.edu; do \
        echo "Fetching GPG key $GPG_KEYS from $server"; \
        gpg --keyserver "$server" --keyserver-options timeout=10 --recv-keys "$GPG_KEYS" && found=yes && break; \
    done; \
    test -z "$found" && echo >&2 "error: failed to fetch GPG key $GPG_KEYS" && exit 1; \
    gpg --batch --verify /tmp/mosq.tar.gz.asc /tmp/mosq.tar.gz; \
    gpgconf --kill all; \
    rm -rf "$GNUPGHOME" /tmp/mosq.tar.gz.asc; \
    mkdir -p /build/mosq; \
    tar --strip=1 -xf /tmp/mosq.tar.gz -C /build/mosq; \
    rm /tmp/mosq.tar.gz

# 编译并安装到 /out（独立根目录）  
RUN set -eux; \
    mkdir -p /out; \
    make -C /build/mosq -j"$(nproc)" \
        CFLAGS="-Wall -O2" \
        WITH_ADNS=no \
        WITH_DOCS=no \
        WITH_SHARED_LIBRARIES=yes \
        WITH_SRV=no \
        WITH_STRIP=yes \
        WITH_WEBSOCKETS=yes \
        prefix=/usr; \
    make -C /build/mosq \
        WITH_ADNS=no WITH_DOCS=no WITH_SHARED_LIBRARIES=yes WITH_SRV=no WITH_STRIP=yes WITH_WEBSOCKETS=yes \
        prefix=/usr DESTDIR=/out install; 
    # 若动态安全插件未随 install 安装，单独安装到 /out
    # make -C /build/mosq/plugins/dynamic-security -j"$(nproc)"; \
    # make -C /build/mosq/plugins/dynamic-security prefix=/usr DESTDIR=/out install || \
    #     install -D -m755 /build/mosq/plugins/dynamic-security/mosquitto_dynamic_security.so /out/usr/lib/mosquitto_dynamic_security.so; \
    # 准备默认配置以兼容 /mosquitto/config 路径
    # mkdir -p /out/mosquitto/config; \
    # if [ -f /out/etc/mosquitto/mosquitto.conf ]; then \
    #     cp -f /out/etc/mosquitto/mosquitto.conf /out/mosquitto/config/mosquitto.conf; \
    # elif [ -f /build/mosq/mosquitto.conf ]; then \
    #     cp -f /build/mosq/mosquitto.conf /out/mosquitto/config/mosquitto.conf; \
    # fi; \
    # # 许可证（可选）
    # mkdir -p /out/usr/share/licenses/mosquitto; \
    # [ -f /build/mosq/epl-v20 ] && install -m644 /build/mosq/epl-v20 /out/usr/share/licenses/mosquitto/epl-v20 || true; \
    # [ -f /build/mosq/edl-v10 ] && install -m644 /build/mosq/edl-v10 /out/usr/share/licenses/mosquitto/edl-v10 || true



# Multi-stage build: build plugin .so then run with mosquitto
FROM golang:latest AS build-plugin

# 必要开发包：插件头文件在 mosquitto-dev；pkg-config 供 cgo 找编译参数
RUN set -eux; \
    apt-get update ;\
    apt-get install -y --no-install-recommends \
      build-essential pkg-config ca-certificates \
      libmosquitto-dev mosquitto-dev \
    ; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
RUN make build


# https://packages.debian.org/search?keywords=mosquitto
FROM debian:trixie-slim

RUN set -eux; \
    apt-get update ;\
    apt-get install -y --no-install-recommends \
      # mosquitto \
      tree \
      ca-certificates tzdata \
      libcjson1 libssl3 libuuid1 libwebsockets19t64 \
    ; \
    rm -rf /var/lib/apt/lists/*

# 运行用户与目录
RUN set -eux; \
    groupadd -r -g 1883 mosquitto || true; \
    useradd  -r -u 1883 -g mosquitto -d /var/empty -s /usr/sbin/nologin -c "mosquitto" mosquitto || true; \
    mkdir -p /mosquitto/config /mosquitto/data /mosquitto/log; \
    chown -R mosquitto:mosquitto /mosquitto

# https://sources.debian.org/src/mosquitto/2.0.22-3/debian/mosquitto.postinst
# RUN set -eux; \
#     install -d -o mosquitto -g mosquitto -m 755 \
#       /mosquitto/config /mosquitto/data /mosquitto/log /mosquitto/plugins

# Copy plugin and example config into the image
COPY --from=build-plugin --chown=mosquitto:mosquitto /src/build/ /mosquitto/plugins/

#COPY docker-entrypoint.sh /
#ENTRYPOINT ["/docker-entrypoint.sh"]
#EXPOSE 1883



# USER mosquitto

# 拷贝构建产物
COPY --from=build-main /out/ /

# 卷、端口、入口
VOLUME ["/mosquitto/data", "/mosquitto/log"]

COPY docker-entrypoint.sh /
EXPOSE 1883
ENTRYPOINT ["/docker-entrypoint.sh"]
# CMD ["/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"]
CMD ["mosquitto"]