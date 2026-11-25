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
         ca-certificates curl bash openssl sqlite3 \
         postfix dovecot-core dovecot-imapd dovecot-lmtpd dovecot-sieve \
    && rm -rf /var/lib/apt/lists/*

# Reserved uid/gid for the Maildir owner mapping (plan §5 待拍板项): keep a
# stable vmail identity so /srv/urmail/data/mail ownership survives upgrades.
# compose.yaml runs urmaild as 5000:5000, and internal/agentd/peercred.go only
# admits the agent's own euid plus the uid of the "mailinabox" account, so the
# runtime stage must carry that account name on the same reserved uid. The
# dovecot configs keep addressing the mail owner as "vmail", hence a non-unique
# uid alias instead of a rename. Do not relax the peer policy instead.
RUN groupadd -g 5000 vmail && useradd -u 5000 -g 5000 -M -N -s /usr/sbin/nologin vmail \
    && useradd -o -u 5000 -g 5000 -M -N -s /usr/sbin/nologin mailinabox \
    && test "$(id -u mailinabox)" = "5000" && getent passwd mailinabox >/dev/null \
    && mkdir -p /srv/urmail/config /srv/urmail/data /var/lib/urmail /run/urmail \
    && mkdir -p /run/urmail-policy && chown 5000:5000 /run/urmail-policy /run/urmail /var/lib/urmail

COPY --from=build /out/bin/urmaild /usr/local/bin/urmaild
# The cluster bootstrap SQL is read by the PostgreSQL process (uid 70) from a
# bind mount or /usr/share/urmail; a restrictive checkout umask must not make it
# unreadable, so pin the mode in the image and assert it here.
COPY --chmod=0644 internal/db/bootstrap.sql /usr/share/urmail/bootstrap.sql
RUN test "$(stat -c '%a' /usr/share/urmail/bootstrap.sql)" = "644" \
    && test -s /usr/share/urmail/bootstrap.sql
COPY --from=build /out/bin/urmailctl /usr/local/bin/urmailctl
COPY --from=build /out/bin/urmaild-agent /usr/local/bin/urmaild-agent
COPY migrations_sqlite /usr/local/share/urmail/migrations_sqlite
RUN chmod 0644 /usr/local/share/urmail/migrations_sqlite/*.sql
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh /usr/local/bin/urmaild \
    /usr/local/bin/urmailctl /usr/local/bin/urmaild-agent

ENV URMAIL_CONFIG_DIR=/srv/urmail/config
WORKDIR /srv/urmail
# urmaild (no args) serves API+webmail; other services override `command`.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["urmaild"]
