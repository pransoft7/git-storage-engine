FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    apache2 git && \
    rm -rf /var/lib/apt/lists/* && \
    a2enmod cgi env alias auth_basic authn_core authn_file authz_core authz_user && \
    mkdir -p /srv/git && \
    chown www-data:www-data /srv/git

COPY git-http.conf /etc/apache2/conf-available/git-http.conf

RUN a2enconf git-http && \
    printf '%s\n' '#!/bin/sh' \
    'exec /usr/sbin/apache2ctl -D FOREGROUND' \
    > /start.sh && chmod +x /start.sh

EXPOSE 80

CMD ["/start.sh"]