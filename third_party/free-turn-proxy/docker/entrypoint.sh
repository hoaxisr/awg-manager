#!/bin/sh
set -e

CONNECT="${CONNECT_ADDR:?CONNECT_ADDR is required}"
LISTEN="${LISTEN_ADDR:-0.0.0.0:56000}"

case "$CONNECT" in
  *:*) ;;
  *) echo "CONNECT_ADDR must be in host:port format" >&2; exit 1 ;;
esac

set -- -listen "$LISTEN" -connect "$CONNECT"

if [ "${MODE}" = "tcp" ]; then
    set -- "$@" -mode tcp
fi

if [ -n "${OBF_PROFILE}" ] && [ "${OBF_PROFILE}" != "none" ]; then
    OBF="${OBF_KEY:?OBF_KEY is required when OBF_PROFILE != none}"
    set -- "$@" -obf-profile "$OBF_PROFILE" -obf-key "$OBF"
fi

if [ -n "${OBF_TIMING}" ]; then
    set -- "$@" -obf-timing "${OBF_TIMING}"
fi

if [ "${DEBUG}" = "true" ]; then
    set -- "$@" -debug
fi

if [ -n "${CLIENTS_FILE}" ]; then
    set -- "$@" -clients-file "${CLIENTS_FILE}"
fi

exec ./server "$@"
