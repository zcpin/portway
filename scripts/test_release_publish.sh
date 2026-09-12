#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/publish_release.sh"

# gh 替身只维护测试状态，不调用网络或真实仓库。
gh() {
  local operation="$2"
  if [ "$1" != release ] || [ "$3" != "$GITHUB_REF_NAME" ]; then
    echo "Unexpected gh arguments: $*" >&2
    return 1
  fi
  case "$operation" in
    view)
      [ "$release_exists" = true ] || return 1
      if [[ " $* " == *' --json isDraft '* ]]; then
        echo "$release_draft"
      fi
      ;;
    create)
      [[ " $* " == *" --prerelease=$PRERELEASE "* ]] || return 1
      if [ "$PRERELEASE" = true ]; then
        [[ " $* " == *' --latest=false '* ]] || return 1
      fi
      release_exists=true
      release_draft=false
      trace+=' create'
      ;;
    upload)
      trace+=' upload'
      [ "$fail_upload" = false ] || return 1
      uploaded=true
      ;;
    edit)
      [ "$uploaded" = true ] || { echo 'Published before uploading' >&2; return 1; }
      [[ " $* " == *' --draft=false '* ]] || return 1
      [[ " $* " == *" --prerelease=$PRERELEASE "* ]] || return 1
      if [ "$PRERELEASE" = true ]; then
        [[ " $* " == *' --latest=false '* ]] || return 1
      fi
      if [ "$keep_draft" = false ]; then release_draft=false; fi
      trace+=' edit'
      ;;
    *) echo "Unexpected operation: $operation" >&2; return 1 ;;
  esac
}

reset_release() {
  release_exists="$1"
  release_draft="$2"
  uploaded=false
  fail_upload=false
  keep_draft=false
  trace=''
}

for PRERELEASE in false true; do
  GITHUB_REF_NAME=v1.2.3
  if [ "$PRERELEASE" = true ]; then GITHUB_REF_NAME=v1.2.3-rc.1; fi
  for initial_state in missing draft published; do
    case "$initial_state" in
      missing) reset_release false true ;;
      draft) reset_release true true ;;
      published) reset_release true false ;;
    esac
    publish_release
    [ "$release_draft" = false ]
    if [ "$initial_state" = missing ]; then
      [ "$trace" = ' create' ]
    else
      [ "$trace" = ' upload edit' ]
    fi
    echo "prerelease=$PRERELEASE initial=$initial_state: passed"
  done
done

reset_release true true
fail_upload=true
if publish_release; then echo 'Upload failure was ignored' >&2; exit 1; fi
[ "$release_draft" = true ]
[ "$trace" = ' upload' ]

reset_release true true
keep_draft=true
if publish_release 2>/dev/null; then echo 'Draft postcondition was not checked' >&2; exit 1; fi
echo 'Upload failure and draft verification: passed'
