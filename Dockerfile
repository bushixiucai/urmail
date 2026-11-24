# syntax=docker/dockerfile:1.7
# urMail compose skeleton image (Phase 2, ARCH-PLAN §1.1).
# Multi-stage: web build -> Go build -> runtime (debian + postfix + dovecot).
# Single image, multiple commands: urmaild / urmailctl / urmaild-agent /
# postfix / dovecot are all provided by this image (plan §5: split services,
# same image).
#
# Build溯源边界 (plan §1.8): when the build context carries a real .git
# directory, both stages embed the true HEAD stamp / VCS metadata. In a git
# worktree checkout .git is a pointer file, git calls fail, the frontend stamp
# falls back to "compose-skeleton" and Go omits VCS stamps. upgrade.sh gate
# work (Phase 2 continuation) must build from a real clone.

# ---------------------------------------------------------------- web build
FROM node:22-alpine AS web
ARG https_proxy=""
ARG http_proxy=""
ENV https_proxy=$https_proxy http_proxy=$http_proxy
WORKDIR /src
COPY .git /src/.git
COPY web/package.json web/package-lock.json /src/web/
RUN apk add --no-cache git \
    && cd web && npm ci --no-audit --no-fund
COPY web/ /src/web/
# npm run build ends with `git rev-parse HEAD > dist/.build-stamp`; when the
# context carries only a worktree .git pointer file that chain fails, so fall
# back to an explicit tsc+vite run with a static stamp (see header note).
RUN cd web && (npm run build || (npx tsc -b && npx vite build && echo "compose-skeleton" > dist/.build-stamp))

# ------------------------------------------------------------- go build
FROM golang:1.27-alpine AS build
ARG VERSION=snapshot
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY CGO_ENABLED=0 GOTOOLCHAIN=auto
RUN apk add --no-cache git bash make
WORKDIR /src
COPY . /src
COPY --from=web /src/web/dist /src/internal/web/dist
# build-go = frontend-check + compile; the frontend stamp comes from stage 1.
# Cache mounts keep the module and build caches across retries (cold networks).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make build-go VERSION="$VERSION" DIST=/out/bin

# ------------------------------------------------------------- runtime
FROM debian:bookworm-slim AS runtime
ARG https_proxy=""
ARG http_proxy=""
RUN apt-get update \
    && env https_proxy=$https_proxy http_proxy=$http_proxy \
       apt-get install -y --no-install-recommends \
         ca-certificates curl bash openssl \
         postfix dovecot-core dovecot-imapd dovecot-lmtpd \
    && rm -rf /var/lib/apt/lists/*

# Reserved uid/gid for the Maildir owner mapping (plan §5 待拍板项): keep a
# stable vmail identity so /srv/urmail/data/mail ownership survives upgrades.
RUN groupadd -g 5000 vmail && useradd -u 5000 -g 5000 -M -N -s /usr/sbin/nologin vmail \
    && mkdir -p /srv/urmail/config /srv/urmail/data \
    && mkdir -p /run/urmail-policy && chown 5000:5000 /run/urmail-policy

COPY --from=build /out/bin/urmaild /usr/local/bin/urmaild
COPY --from=build /out/bin/urmailctl /usr/local/bin/urmailctl
COPY --from=build /out/bin/urmaild-agent /usr/local/bin/urmaild-agent
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh /usr/local/bin/urmaild \
    /usr/local/bin/urmailctl /usr/local/bin/urmaild-agent

ENV URMAIL_CONFIG_DIR=/srv/urmail/config
WORKDIR /srv/urmail
# urmaild (no args) serves API+webmail; other services override `command`.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["urmaild"]
