FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm@sha256:47ce5636e9936b2c5cbf708925578ef386b4f8872aec74a67bd13a627d242b19 AS source

WORKDIR /src/garm
RUN git init . \
    && git remote add origin https://github.com/cloudbase/garm.git \
    && git fetch --depth=1 origin 33ec0921418a470ca7e00dfb20ae111595da24e1 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = 33ec0921418a470ca7e00dfb20ae111595da24e1
COPY patches/garm-gitea-ephemeral-completion.patch /tmp/gitea-completion.patch
RUN echo '089a4e80fda3d8a99399eaae68118ef2989ab739aeba0a5a0777b4d129e01a34  /tmp/gitea-completion.patch' | sha256sum -c - \
    && git apply --check /tmp/gitea-completion.patch \
    && git apply /tmp/gitea-completion.patch
COPY patches/garm-creation-completion-race.patch /tmp/creation-completion.patch
RUN echo '6c0c8238b40f9d502c023d2987d987a6ac14ff55a3e7bdafd36e1ce26d779f84  /tmp/creation-completion.patch' | sha256sum -c - \
    && git apply --check /tmp/creation-completion.patch \
    && git apply /tmp/creation-completion.patch

FROM --platform=$BUILDPLATFORM node:24.19.0-bookworm-slim@sha256:a9f5f7c91a432850b2a8a7797adf5eadb6c733ceed61167806cee7ea7fbc29df AS webapp
WORKDIR /src/webapp
COPY --from=source /src/garm/webapp/ ./
RUN npm ci && npm run build

FROM source AS builder
ARG TARGETARCH
RUN test "$TARGETARCH" = amd64 \
    && apt-get update \
    && apt-get install -y --no-install-recommends gcc-x86-64-linux-gnu libc6-dev-amd64-cross \
    && rm -rf /var/lib/apt/lists/*
COPY --from=webapp /src/webapp/build/ /src/garm/webapp/assets/
ENV CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=x86_64-linux-gnu-gcc GOTOOLCHAIN=local
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -mod=vendor -trimpath -tags osusergo,netgo,sqlite_omit_load_extension \
      -ldflags '-s -w -X github.com/cloudbase/garm/util/appdefaults.Version=33ec0921-agent37-lifecycle-fixes' \
      -o /out/garm ./cmd/garm \
    && go build -mod=vendor -trimpath -tags osusergo,netgo,sqlite_omit_load_extension \
      -ldflags '-s -w -X github.com/cloudbase/garm/util/appdefaults.Version=33ec0921-agent37-lifecycle-fixes' \
      -o /out/garm-cli ./cmd/garm-cli

WORKDIR /src/provider
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY main.go ./
COPY bootstrap/ ./bootstrap/
COPY client/ ./client/
COPY config/ ./config/
COPY tailscale/ ./tailscale/
COPY provider/ ./provider/
ARG PROVIDER_REVISION=development
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/jtatum/garm-provider-agent37/provider.Version=${PROVIDER_REVISION}" \
      -o /out/garm-provider-agent37 .

FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3
ARG PROVIDER_REVISION=development
LABEL org.opencontainers.image.source="https://github.com/jtatum/garm-provider-agent37" \
      org.opencontainers.image.revision="${PROVIDER_REVISION}" \
      org.opencontainers.image.title="GARM Agent37 controller" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.garm.source.revision="33ec0921418a470ca7e00dfb20ae111595da24e1" \
      io.garm.gitea-completion-patch.sha256="089a4e80fda3d8a99399eaae68118ef2989ab739aeba0a5a0777b4d129e01a34" \
      io.garm.creation-completion-patch.sha256="6c0c8238b40f9d502c023d2987d987a6ac14ff55a3e7bdafd36e1ce26d779f84"
COPY --from=builder /out/garm /out/garm-cli /usr/local/bin/
COPY --from=builder /out/garm-provider-agent37 /opt/garm/providers.d/garm-provider-agent37
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY templates/gitea-agent37.py.tmpl /opt/garm/templates/gitea-agent37.py.tmpl
COPY LICENSE /usr/share/licenses/garm-provider-agent37/LICENSE
COPY --from=source /src/garm/LICENSE /usr/share/licenses/garm/LICENSE
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3 \
    && dpkg-query -W > /opt/garm/packages.txt \
    && rm -rf /var/lib/apt/lists/* \
    && printf 'garm:x:10001:10001:GARM controller:/var/lib/garm:/usr/sbin/nologin\n' >> /etc/passwd \
    && printf 'garm:x:10001:\n' >> /etc/group \
    && install -d -m 0700 -o 10001 -g 10001 /var/lib/garm \
    && install -d -m 0755 /etc/garm
ENV HOME=/var/lib/garm
USER 10001:10001
WORKDIR /var/lib/garm
EXPOSE 9997
ENTRYPOINT ["/usr/local/bin/garm", "--config", "/etc/garm/config.toml"]
