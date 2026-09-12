#!/usr/bin/env bash

publish_release() {
  local tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
  local prerelease="${PRERELEASE:?PRERELEASE is required}"
  local release_flags=(--prerelease="$prerelease")
  case "$prerelease" in
    true) release_flags+=(--latest=false) ;;
    false) ;;
    *) echo "Invalid prerelease value: $prerelease" >&2; return 1 ;;
  esac

  if gh release view "$tag" >/dev/null 2>&1; then
    # 草稿保持隐藏，直到全部附件上传成功。
    gh release upload "$tag" release-assets/* --clobber || return
    gh release edit "$tag" --draft=false "${release_flags[@]}" || return
  else
    gh release create "$tag" release-assets/* --verify-tag --generate-notes "${release_flags[@]}" || return
  fi

  local draft
  draft=$(gh release view "$tag" --json isDraft --jq '.isDraft') || return
  if [ "$draft" != false ]; then
    echo "Release is still a draft: $tag" >&2
    return 1
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
  publish_release
fi
