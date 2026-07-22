#!/bin/bash
# Usage: ./client -debug ... 2>&1 | ./scripts/routes.sh
set -euo pipefail

gateway="$(ip -o -4 route show to default | awk '/via/ {print $3}' | head -1)"
if [[ -z "${gateway}" ]]; then
  echo "Could not determine default gateway" >&2
  exit 1
fi

ip_cmd=(ip)
if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  ip_cmd=(sudo ip)
fi

while IFS= read -r line; do
  line="${line%$'\r'}"

  remote=""
  if [[ "$line" =~ TURN\ server\ IP:\ (([0-9]{1,3}\.){3}[0-9]{1,3}) ]]; then
    remote="${BASH_REMATCH[1]}"
  elif [[ "$line" =~ relayed-address=(([0-9]{1,3}\.){3}[0-9]{1,3}):[0-9]+ ]]; then
    remote="${BASH_REMATCH[1]}"
  elif [[ "$line" =~ ^(([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2})$ ]]; then
    remote="$line"
  elif [[ "$line" =~ ^(([0-9]{1,3}\.){3}[0-9]{1,3})$ ]]; then
    remote="$line"
  fi

  [[ -z "$remote" ]] && continue

  echo "Ensuring route to $remote via $gateway"
  "${ip_cmd[@]}" route replace "$remote" via "$gateway"
done
