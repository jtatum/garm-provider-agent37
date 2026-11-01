FROM ubuntu:24.04@sha256:78d4744ead1f5b19b04d0df558ab182a5300903900755e515b2ca4d10eb2044d

ENV HOME=/root
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git python3 xz-utils tini iptables iproute2 kmod \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
    && printf '%s\n' 'deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable' > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
      docker-ce=5:29.1.3-1~ubuntu.24.04~noble \
      docker-ce-cli=5:29.1.3-1~ubuntu.24.04~noble \
      containerd.io=2.1.5-1~ubuntu.24.04~noble \
      docker-buildx-plugin=0.30.1-1~ubuntu.24.04~noble \
      docker-compose-plugin=2.40.3-1~ubuntu.24.04~noble \
    && mkdir -p /opt/garm/cache \
    && dpkg-query -W > /opt/garm/packages.txt \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://gitea.com/gitea/runner/releases/download/v3.5.0/gitea-runner-3.5.0-linux-amd64.xz -o /tmp/runner.xz \
    && echo '2d0752f48a9166239b3a904c56527aae9ae6ca210d6587259c64407ef702ac27  /tmp/runner.xz' | sha256sum -c - \
    && xz -dc /tmp/runner.xz > /usr/local/bin/gitea-runner \
    && chmod 0755 /usr/local/bin/gitea-runner \
    && echo '16dc5c89ad5ac5496369642f01ed29c9bb130a2512e16a8cc3b400d9d0bfea73  /usr/local/bin/gitea-runner' | sha256sum -c - \
    && rm /tmp/runner.xz

COPY alpine.tar /opt/garm/cache/alpine.tar
RUN echo '3c1b545ef346526439a40ab87bf252f21c0e5591e3d181d63bd0a6436a745bb3  /opt/garm/cache/alpine.tar' | sha256sum -c -
COPY entrypoint.py /opt/garm/entrypoint.py
WORKDIR /root
ENTRYPOINT ["/usr/bin/tini", "--", "python3", "/opt/garm/entrypoint.py"]
