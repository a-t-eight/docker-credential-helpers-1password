#!/usr/bin/env bash
set -euo pipefail

STATE="${OP_MOCK_STATE:-${RUNNER_TEMP:-/tmp}/op-mock.json}"
mkdir -p "$(dirname "$STATE")"
touch "$STATE"
jq . >/dev/null 2>&1 || { echo "jq required"; exit 1; }

init_state() {
  if ! jq . "$STATE" >/dev/null 2>&1; then echo '[]' >"$STATE"; fi
}
init_state

# helpers
new_id() { echo "itm_$(date +%s%N)_$RANDOM"; }
list_items() { jq -c '.[]' "$STATE"; }
save_items() { tmp=$(mktemp); cat >"$tmp"; mv "$tmp" "$STATE"; }
get_item() { jq -c --arg id "$1" '.[] | select(.id==$id)' "$STATE"; }

case "${1:-}" in
  --version)
    echo "1.13.0"
    ;;

  item)
    sub="${2:-}"
    case "$sub" in
      list)
        # expect: item list --tags docker-credential-helpers --format json
        echo '['
        first=1
        while read -r line; do
          id=$(echo "$line" | jq -r '.id')
          title=$(echo "$line" | jq -r '.title')
          if [ "$first" -eq 0 ]; then echo ','; fi
          printf '{"id":"%s","title":"%s","tags":["docker-credential-helpers"]}' "$id" "$title"
          first=0
        done < <(list_items)
        echo ']'
        ;;

      get)
        id="$3"
        if [[ "${4:-}" == "--field" ]]; then
          field="$5"
          val=$(get_item "$id" | jq -r --arg f "$field" '.fields[] | select(.label==$f) | .value')
          [ -n "$val" ] || exit 1
          echo "$val"
        else
          it=$(get_item "$id")
          [ -n "$it" ] || exit 1
          echo "$it"
        fi
        ;;

      create)
        # parse: --category login --title X --url S --tags T username=U password=P
        title=""
        url=""
        user=""
        pass=""
        shift 2
        while [[ $# -gt 0 ]]; do
          case "$1" in
            --title) title="$2"; shift 2;;
            --url) url="$2"; shift 2;;
            --tags) shift 2;; # ignore tag value
            --category) shift 2;;
            username=*) user="${1#username=}"; shift;;
            password=*) pass="${1#password=}"; shift;;
            *) shift;;
          esac
        done
        id=$(new_id)
        # build item JSON
        items=$(jq -n --arg id "$id" --arg title "$title" --arg url "$url" \
                    --arg user "$user" --arg pass "$pass" \
                    '[{"id":$id,"title":$title,"urls":[{"href":$url}],"fields":[{"label":"username","value":$user},{"label":"password","value":$pass}]}]')
        # append
        jq -c --argjson new "$items" '. + $new' "$STATE" | save_items
        echo "$id"
        ;;

      edit)
        id="$3"; shift 3
        newurl=""; newuser=""; newpass=""
        while [[ $# -gt 0 ]]; do
          case "$1" in
            --url) newurl="$2"; shift 2;;
            username=*) newuser="${1#username=}"; shift;;
            password=*) newpass="${1#password=}"; shift;;
            --vault) shift 2;;
            *) shift;;
          esac
        done
        jq -c --arg id "$id" --arg u "$newurl" --arg user "$newuser" --arg pass "$newpass" '
          map(if .id==$id then
               .urls = (if $u!="" then [{"href":$u}] else .urls end)
               | .fields = ( [ .fields[]
                               | if .label=="username" and $user!="" then .value=$user else . end
                             ] | ( . + (if $pass!="" then [] else [] end) ))
               | (.fields |= (map(if .label=="password" and $pass!="" then .value=$pass else . end)))
             else . end)
        ' "$STATE" | save_items
        ;;

      delete)
        id="$3"
        jq -c --arg id "$id" 'map(select(.id!=$id))' "$STATE" | save_items
        ;;

      *)
        echo "unsupported: item $sub" >&2; exit 2;;
    esac
    ;;

  *)
    echo "unsupported: $*" >&2; exit 2;;
esac