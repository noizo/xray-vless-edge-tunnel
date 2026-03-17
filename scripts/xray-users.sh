#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
SECRET="$DIR/secret.yaml"
NS="xray"
DEPLOY="xray"
HOST="hide.nikolaev.id"
PORT=443

read_users() {
  sed -n '/users.json/,/^[^ ]/p' "$SECRET" \
    | grep -oE '\{"id": "[^"]+", "name": "[^"]+"\}' \
    | sed 's/{"id": "//; s/", "name": "/=/; s/"}//' \
    || true
}

write_secret() {
  local entries=()
  while IFS='=' read -r uuid name; do
    [[ -z "$uuid" ]] && continue
    entries+=("      {\"id\": \"$uuid\", \"name\": \"$name\"}")
  done

  local joined=""
  if [[ ${#entries[@]} -gt 0 ]]; then
    joined=$(printf ',\n%s' "${entries[@]}")
    joined=${joined:2}
  fi

  cat > "$SECRET" <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: xray-users
type: Opaque
stringData:
  users.json: |
    [
${joined}
    ]
EOF
}

seek() {
  local users
  users=$(read_users)
  if [[ -z "$users" ]]; then
    echo "no users"
    return
  fi
  printf "\n%-20s %s\n" "NAME" "UUID"
  printf "%-20s %s\n" "----" "----"
  echo "$users" | while IFS='=' read -r uuid name; do
    printf "%-20s %s\n" "$name" "$uuid"
  done
  echo
}

hide() {
  local added=0
  while true; do
    read -rep "name to hide: " name
    [[ -z "$name" ]] && echo "empty name, aborting" && return

    if read_users | cut -d'=' -f2 | grep -qx "$name" 2>/dev/null; then
      echo "name '$name' already exists"
      return
    fi

    uuid=$(uuidgen | tr '[:upper:]' '[:lower:]')
    { read_users; echo "${uuid}=${name}"; } | write_secret
    printf "\nadded: %s → %s\n\n" "$name" "$uuid"
    added=$((added + 1))

    echo "add another?"
    tput civis
    local opts=("yes" "no") cur=0
    draw_menu $cur "${opts[@]}"
    while true; do
      read -rsn1 key < /dev/tty
      if [[ "$key" == $'\x1b' ]]; then
        read -rsn1 -t 0.01 _k1 < /dev/tty || true
        read -rsn1 -t 0.01 k2 < /dev/tty || true
        case "$k2" in
          A) [[ $cur -gt 0 ]] && cur=$((cur - 1)) ;;
          B) [[ $cur -lt 1 ]] && cur=$((cur + 1)) ;;
        esac
      elif [[ "$key" == "" ]]; then
        break
      fi
      tput cuu 2
      draw_menu $cur "${opts[@]}"
    done
    tput cnorm

    [[ "${opts[$cur]}" == "no" ]] && break
    echo
  done

  [[ $added -gt 0 ]] && reload_cluster
}

obliterate() {
  local removed=0
  while true; do
    local users=()
    while IFS='=' read -r uuid name; do
      [[ -n "$uuid" ]] && users+=("$uuid=$name")
    done < <(read_users)

    if [[ ${#users[@]} -eq 0 ]]; then
      echo "no users to obliterate"
      break
    fi

    printf "\n"
    local names=()
    for i in "${!users[@]}"; do
      names+=("${users[$i]#*=}")
    done

    tput civis
    local cur=0 total=${#names[@]}
    draw_menu $cur "${names[@]}"

    while true; do
      read -rsn1 key < /dev/tty
      if [[ "$key" == $'\x1b' ]]; then
        read -rsn1 -t 0.01 _k1 < /dev/tty || true
        read -rsn1 -t 0.01 k2 < /dev/tty || true
        case "$k2" in
          A) [[ $cur -gt 0 ]] && cur=$((cur - 1)) ;;
          B) [[ $cur -lt $((total - 1)) ]] && cur=$((cur + 1)) ;;
        esac
      elif [[ "$key" == "" ]]; then
        break
      fi
      tput cuu "$total"
      draw_menu $cur "${names[@]}"
    done
    tput cnorm

    local target="${names[$cur]}"
    { read_users | grep -v "=${target}$" || true; } | write_secret
    printf "\nobliterated: %s\n\n" "$target"
    removed=$((removed + 1))

    # check if any users left
    if [[ -z "$(read_users)" ]]; then
      break
    fi

    echo "obliterate another?"
    tput civis
    local opts=("yes" "no") oc=0
    draw_menu $oc "${opts[@]}"
    while true; do
      read -rsn1 key < /dev/tty
      if [[ "$key" == $'\x1b' ]]; then
        read -rsn1 -t 0.01 _k1 < /dev/tty || true
        read -rsn1 -t 0.01 k2 < /dev/tty || true
        case "$k2" in
          A) [[ $oc -gt 0 ]] && oc=$((oc - 1)) ;;
          B) [[ $oc -lt 1 ]] && oc=$((oc + 1)) ;;
        esac
      elif [[ "$key" == "" ]]; then
        break
      fi
      tput cuu 2
      draw_menu $oc "${opts[@]}"
    done
    tput cnorm

    [[ "${opts[$oc]}" == "no" ]] && break
  done

  [[ $removed -gt 0 ]] && reload_cluster
}

share() {
  if ! command -v qrencode &>/dev/null; then
    echo "qrencode not found, install with: brew install qrencode"
    return
  fi

  local users=()
  while IFS='=' read -r uuid name; do
    [[ -n "$uuid" ]] && users+=("$uuid=$name")
  done < <(read_users)

  if [[ ${#users[@]} -eq 0 ]]; then
    echo "no users to share"
    return
  fi

  printf "\n"
  local names=()
  for i in "${!users[@]}"; do
    names+=("${users[$i]#*=}")
  done

  tput civis
  local cur=0 total=${#names[@]}
  draw_menu $cur "${names[@]}"

  while true; do
    read -rsn1 key < /dev/tty
    if [[ "$key" == $'\x1b' ]]; then
      read -rsn1 -t 0.01 _k1 < /dev/tty || true
      read -rsn1 -t 0.01 k2 < /dev/tty || true
      case "$k2" in
        A) [[ $cur -gt 0 ]] && cur=$((cur - 1)) ;;
        B) [[ $cur -lt $((total - 1)) ]] && cur=$((cur + 1)) ;;
      esac
    elif [[ "$key" == "" ]]; then
      break
    fi
    tput cuu "$total"
    draw_menu $cur "${names[@]}"
  done
  tput cnorm

  local target="${names[$cur]}"
  local uuid="${users[$cur]%=*}"
  local uri="vless://${uuid}@${HOST}:${PORT}?encryption=none&security=tls&sni=${HOST}&type=ws&host=${HOST}&path=%2Fws#OCI-${target}"

  printf "\n%s\n\n" "$uri"
  qrencode -t ANSIUTF8 "$uri"
}

reload_cluster() {
  if kubectl get deployment/"$DEPLOY" -n "$NS" --request-timeout=1s &>/dev/null; then
    echo "applying config and restarting xray..."
    kubectl apply -f "$DIR/configmap.yaml" -n "$NS"
    kubectl apply -f "$SECRET" -n "$NS"
    kubectl rollout restart deployment/"$DEPLOY" -n "$NS"
    kubectl rollout status deployment/"$DEPLOY" -n "$NS" --timeout=60s
    echo "done"
  else
    echo "cluster unreachable, files updated locally"
  fi
}

trap 'tput cnorm; echo; exit 0' INT

draw_menu() {
  local cur=$1; shift
  local options=("$@")
  for i in "${!options[@]}"; do
    tput el
    if [[ $i -eq $cur ]]; then
      printf "  \033[7m %s \033[0m\n" "${options[$i]}"
    else
      printf "   %s\n" "${options[$i]}"
    fi
  done
}

options=("seek" "hide" "obliterate" "share")
cur=0
total=${#options[@]}

tput civis
draw_menu $cur "${options[@]}"

while true; do
  read -rsn1 key < /dev/tty
  if [[ "$key" == $'\x1b' ]]; then
    read -rsn1 -t 0.01 _k1 < /dev/tty || true
    read -rsn1 -t 0.01 k2 < /dev/tty || true
    case "$k2" in
      A) [[ $cur -gt 0 ]] && cur=$((cur - 1)) ;;
      B) [[ $cur -lt $((total - 1)) ]] && cur=$((cur + 1)) ;;
    esac
  elif [[ "$key" == "" ]]; then
    break
  fi
  tput cuu "$total"
  draw_menu $cur "${options[@]}"
done

tput cnorm
case "${options[$cur]}" in
  seek) seek ;;
  hide) hide ;;
  obliterate) obliterate ;;
  share) share ;;
esac
