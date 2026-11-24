# LOCAL TEST ONLY: OpenDKIM stand-in for the not-yet-built urmail-milter.
# This image never ships; it proves the compose milter network path and gives
# the synthetic loop a real DKIM-Signature header.
FROM debian:bookworm-slim
ARG https_proxy=""
ARG http_proxy=""
RUN apt-get update \
    && env https_proxy=$https_proxy http_proxy=$http_proxy \
       apt-get install -y --no-install-recommends opendkim opendkim-tools openssl bash \
    && rm -rf /var/lib/apt/lists/*
COPY opendkim.conf /etc/opendkim/opendkim.conf
COPY entrypoint.sh /usr/local/bin/opendkim-entrypoint.sh
RUN chmod 0755 /usr/local/bin/opendkim-entrypoint.sh
EXPOSE 12301
ENTRYPOINT ["/usr/local/bin/opendkim-entrypoint.sh"]
