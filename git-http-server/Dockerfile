FROM golang:1.24-bookworm AS engine-build
WORKDIR /src
COPY engine/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/toyengine ./cmd/toyengine

FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    apache2 git && \
    rm -rf /var/lib/apt/lists/* && \
    a2enmod cgid env alias auth_basic authn_core authn_file authz_core authz_user && \
    mkdir -p /srv/git && \
    chown www-data:www-data /srv/git

COPY git-http-server/git-http.conf /etc/apache2/conf-available/git-http.conf

RUN a2enconf git-http && \
    printf '%s\n' '#!/bin/sh' \
    'exec /usr/sbin/apache2ctl -D FOREGROUND' \
    > /start.sh && chmod +x /start.sh

# Toy storage engine: the binary Git hooks call, and its data dir
# (store/ = the shared "S3", node/ = this server's local state).
COPY --from=engine-build /out/toyengine /usr/local/bin/toyengine
RUN mkdir -p /var/lib/toyengine && chown www-data:www-data /var/lib/toyengine

EXPOSE 80

CMD ["/start.sh"]
