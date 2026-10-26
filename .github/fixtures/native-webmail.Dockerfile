FROM ubuntu:22.04

# The acceptance runner starts each daemon explicitly inside a network-isolated
# disposable container. Package installation must not start host-style services.
RUN printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d \
    && chmod 0755 /usr/sbin/policy-rc.d \
    && apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        dovecot-imapd \
        dovecot-lmtpd \
        openssl \
        postfix \
        postgresql-14 \
        python3 \
        util-linux \
    && rm -rf /var/lib/apt/lists/*

CMD ["sleep", "infinity"]
