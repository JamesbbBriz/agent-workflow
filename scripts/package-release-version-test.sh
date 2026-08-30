#!/bin/sh
set -eu

assert_valid() {
	output=$(scripts/package-release.sh "$1" unsupported target /tmp/unused 2>&1 || true)
	case "$output" in
		*"unsupported release target"*) ;;
		*) echo "expected valid SemVer: $1" >&2; exit 1 ;;
	esac
}

assert_invalid() {
	output=$(scripts/package-release.sh "$1" unsupported target /tmp/unused 2>&1 || true)
	case "$output" in
		*"version must be a v-prefixed semantic version"*) ;;
		*) echo "expected invalid SemVer: $1" >&2; exit 1 ;;
	esac
}

for version in v0.2.0 v1.0.0-alpha v1.0.0-alpha.1+build.5; do
	assert_valid "$version"
done
for version in v01.2.3 v1.02.3 v1.2.03 v1.2.3-01 v1.2.3.4 v1.2 v1.2.3+ v1.2.3_; do
	assert_invalid "$version"
done

first=$(mktemp -d)
second=$(mktemp -d)
third=$(mktemp -d)
fourth=$(mktemp -d)
trap 'rm -rf "$first" "$second" "$third" "$fourth"' EXIT HUP INT TERM
(umask 022; scripts/package-release.sh v0.2.0 linux amd64 "$first")
(umask 077; scripts/package-release.sh v0.2.0 linux amd64 "$second")
cmp "$first/agent-workflow_0.2.0_linux_amd64.tar.gz" "$second/agent-workflow_0.2.0_linux_amd64.tar.gz"
(umask 022; scripts/package-release.sh v0.2.0 windows amd64 "$third")
(umask 077; scripts/package-release.sh v0.2.0 windows amd64 "$fourth")
cmp "$third/agent-workflow_0.2.0_windows_amd64.zip" "$fourth/agent-workflow_0.2.0_windows_amd64.zip"
