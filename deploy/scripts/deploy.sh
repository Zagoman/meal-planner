#!/usr/bin/env bash
# Pull + up the stack on the server and wait until it is healthy.
# Usage (normally via make): deploy.sh <ssh_target> <stack_dir> <expected_sha> <domain>
set -euo pipefail
target=$1 stack_dir=$2 sha=$3 domain=$4
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
sum=$(shasum -a 256 "$here/../stack/compose.yml" | cut -d' ' -f1)

echo "==> deploying $sha to $target:$stack_dir"
ssh -o BatchMode=yes "$target" \
	"HEALTH_TIMEOUT=${HEALTH_TIMEOUT:-120} SKIP_PROXY_CHECK=${SKIP_PROXY_CHECK:-0} bash -s -- '$stack_dir' '$sha' '$domain' '$sum'" \
	<"$here/remote-deploy.sh"
