#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
source_dir="$repo_root/web/dist"
target_dir="$repo_root/internal/webassets/dist"

test -f "$source_dir/index.html"
mkdir -p "$target_dir"
find "$target_dir" -mindepth 1 -depth -delete
cp -R "$source_dir"/. "$target_dir"/
