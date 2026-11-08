#!/usr/bin/env bash
# Install Go tools, retrying flaky downloads.
#
# The module proxy in front of the build sometimes drops a connection while
# a tool is being fetched ("GOAWAY"), which turns a network hiccup into a
# failed job.  Retrying with a short exponential backoff keeps the job
# alive; a tool that is already present (and recorded in the marker file)
# is not rebuilt at all, which is what makes the cached ~/go/bin directory
# worth keeping.
#
# usage: ci-go-install.sh tool@version [tool@version ...]
set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "usage: $0 tool@version [tool@version ...]" >&2
  exit 2
fi

max_attempts=3
gobin="$(go env GOPATH)/bin"
markers="${gobin}/.ci-tools"
mkdir -p "${markers}"

for spec in "$@"; do
  name="${spec%@*}"
  name="${name##*/}"
  version="${spec##*@}"
  marker="${markers}/${name}@${version}"

  if [ -x "${gobin}/${name}" ] && [ -f "${marker}" ]; then
    echo "${name} ${version} is already installed"
    continue
  fi

  attempt=1
  while true; do
    echo "installing ${spec} (attempt ${attempt}/${max_attempts})"
    if go install "${spec}"; then
      break
    fi
    if [ "${attempt}" -ge "${max_attempts}" ]; then
      echo "go install ${spec} failed after ${max_attempts} attempts" >&2
      echo "GOPROXY=$(go env GOPROXY)" >&2
      exit 1
    fi
    backoff=$((5 * 2 ** (attempt - 1)))
    echo "retrying ${spec} in ${backoff}s" >&2
    sleep "${backoff}"
    attempt=$((attempt + 1))
  done

  if [ ! -x "${gobin}/${name}" ]; then
    echo "go install ${spec} did not produce ${gobin}/${name}" >&2
    exit 1
  fi
  : > "${marker}"
done

for spec in "$@"; do
  name="${spec%@*}"
  name="${name##*/}"
  echo "installed: ${gobin}/${name}"
done
