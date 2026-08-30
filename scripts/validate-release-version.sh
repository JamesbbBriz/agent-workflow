#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <version>" >&2
	exit 2
fi

semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if ! printf '%s\n' "$1" | grep -Eq "$semver"; then
	echo "version must be a v-prefixed semantic version" >&2
	exit 2
fi
