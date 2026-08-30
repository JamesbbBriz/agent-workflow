#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: $0 <version> <goos> <goarch> <output-dir>" >&2
	exit 2
fi

version=$1
target_os=$2
target_arch=$3
output=$4
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if ! printf '%s\n' "$version" | grep -Eq "$semver"; then
	echo "version must be a v-prefixed semantic version" >&2
	exit 2
fi
case "$target_os/$target_arch" in
	linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64) ;;
	*) echo "unsupported release target: $target_os/$target_arch" >&2; exit 2 ;;
esac

mkdir -p "$output"
output=$(cd "$output" && pwd)
stage_root=$(mktemp -d)
trap 'rm -rf "$stage_root"' EXIT HUP INT TERM
name="agent-workflow_${version#v}_${target_os}_${target_arch}"
bundle="$stage_root/$name"
mkdir -p "$bundle"

extension=
if [ "$target_os" = windows ]; then
	extension=.exe
fi

for command in agent-workflow agent-workflow-codex agent-workflow-claude-code agent-workflow-pi agent-workflow-openclaw agent-workflow-hermes; do
	ldflags="-s -w"
	if [ "$command" = agent-workflow ]; then
		ldflags="$ldflags -X github.com/JamesbbBriz/agent-workflow/internal/cli.buildVersion=$version"
	fi
	CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags "$ldflags" -o "$bundle/$command$extension" "./cmd/$command"
done

cp LICENSE README.md SECURITY.md COMPATIBILITY.md "$bundle/"
if [ "$target_os" = windows ]; then
	go run ./scripts/package-archive.go -source "$bundle" -output "$output/$name.zip"
else
	go run ./scripts/package-archive.go -source "$bundle" -output "$output/$name.tar.gz"
fi
