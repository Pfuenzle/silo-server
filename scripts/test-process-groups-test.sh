#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
source "$root/scripts/test-process-groups.sh"
temp="$(mktemp -d "${TMPDIR:-/tmp}/silo-process-groups.XXXXXX")"
registry="$temp/process-groups"
ready="$temp/ready"
events="$temp/events"
group=""
run_a=""
run_b=""

cleanup() {
  if [[ "$group" =~ ^[1-9][0-9]*$ ]]; then
    kill -KILL -- "-$group" 2>/dev/null || true
  fi
  [[ "$run_a" =~ ^silo-[a-f0-9]{32}$ ]] && docker_cleanup_run "$run_a" 2>/dev/null || true
  [[ "$run_b" =~ ^silo-[a-f0-9]{32}$ ]] && docker_cleanup_run "$run_b" 2>/dev/null || true
  rm -rf "$temp"
}
trap cleanup EXIT INT TERM

setsid python3 -c 'import signal,socket,sys,time; events=sys.argv[2]; handler=lambda name: (lambda *_: open(events, "a").write(name+"\n")); signal.signal(signal.SIGTERM, handler("TERM")); signal.signal(signal.SIGINT, handler("INT")); listener=socket.socket(); listener.bind(("127.0.0.1", 0)); listener.listen(); open(sys.argv[1], "w").write(str(listener.getsockname()[1])); time.sleep(60)' "$ready" "$events" &
group="$!"
mkdir -m 700 "$registry"

for _ in {1..100}; do
  [[ -s "$ready" ]] && break
  sleep 0.01
done
[[ -s "$ready" ]]
port="$(<"$ready")"
starttime="$(silo_process_starttime "$group")"
own_group="$(ps -o pgid= -p "$$" | tr -d '[:space:]')"
own_starttime="$(silo_process_starttime "$own_group")"
printf '%s\n' '1' > "$registry/$group"
printf '%s\n' "$own_starttime" > "$registry/$own_group"
printf '%s\n' 'not-a-starttime' > "$registry/999999"

cleanup_silo_test_process_groups "$registry"
if ! kill -0 -- "-$group" 2>/dev/null; then
  printf 'stale process identity was signaled: %s\n' "$group" >&2
  exit 1
fi
[[ ! -e "$registry/$group" && ! -e "$registry/$own_group" ]]
printf '%s\n' "$starttime" > "$registry/$group"

cleanup_silo_test_process_groups "$registry"

if kill -0 -- "-$group" 2>/dev/null; then
  printf 'registered process group survived cleanup: %s\n' "$group" >&2
  exit 1
fi
python3 -c 'import socket,sys; listener=socket.socket(); listener.bind(("127.0.0.1", int(sys.argv[1]))); listener.close()' "$port"
[[ "$(<"$events")" == $'TERM\nINT' ]]

cleanup_silo_test_process_groups() { return 1; }
if (silo_cleanup_status 7 "$registry"); then exit 1; else [[ "$?" -eq 7 ]]; fi
if (silo_cleanup_status 0 "$registry"); then exit 1; else [[ "$?" -eq 1 ]]; fi

docker_cleanup_run() {
  local run_id="$1"
  docker ps -aq --filter "label=silo.oidc.test=$run_id" | xargs -r docker rm -f >/dev/null
  docker network ls -q --filter "label=silo.oidc.test=$run_id" | xargs -r docker network rm >/dev/null
}

if docker info >/dev/null 2>&1; then
  run_a="silo-$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')"
  run_b="silo-$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')"
  network_a="silo-test-${run_a##silo-}"
  network_b="silo-test-${run_b##silo-}"
  docker network create --label "silo.oidc.test=$run_a" "$network_a" >/dev/null
  docker network create --label "silo.oidc.test=$run_b" "$network_b" >/dev/null
  docker run -d --rm --label "silo.oidc.test=$run_a" --network "$network_a" busybox:1.36 sleep 60 >/dev/null
  docker run -d --rm --label "silo.oidc.test=$run_b" --network "$network_b" busybox:1.36 sleep 60 >/dev/null
  docker_cleanup_run "$run_a"
  [[ -z "$(docker ps -aq --filter "label=silo.oidc.test=$run_a")" && -z "$(docker network ls -q --filter "label=silo.oidc.test=$run_a")" ]]
  [[ -n "$(docker ps -aq --filter "label=silo.oidc.test=$run_b")" && -n "$(docker network ls -q --filter "label=silo.oidc.test=$run_b")" ]]
  docker_cleanup_run "$run_b"
else
  printf '%s\n' 'docker_isolation=skipped docker_unavailable'
fi
printf 'process_group_cleanup=clean group=%s listener_port=%s\n' "$group" "$port"
