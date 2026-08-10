#!/usr/bin/env bash

silo_process_starttime() {
  local pid="$1"
  local stat
  [[ "$pid" =~ ^[2-9][0-9]*$ ]] || return 1
  stat="$(<"/proc/$pid/stat")" || return 1
  stat="${stat#*) }"
  local fields=()
  read -r -a fields <<< "$stat"
  [[ "${fields[19]:-}" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s\n' "${fields[19]}"
}

cleanup_silo_test_process_groups() {
  local registry_dir="$1"
  local own_group
  own_group="$(ps -o pgid= -p "$$" | tr -d '[:space:]')"
  local entry pid expected actual
  local groups=()
  [[ -d "$registry_dir" ]] || return 0

  while IFS= read -r -d '' entry; do
    pid="${entry##*/}"
    expected="$(<"$entry")"
    if [[ ! "$pid" =~ ^[2-9][0-9]*$ ]] || [[ ! "$expected" =~ ^[1-9][0-9]*$ ]] || [[ "$pid" == "$own_group" ]]; then
      rm -f "$entry"
      continue
    fi
    actual="$(silo_process_starttime "$pid" 2>/dev/null || true)"
    if [[ "$actual" != "$expected" ]]; then
      rm -f "$entry"
      continue
    fi
    groups+=("$pid")
  done < <(find "$registry_dir" -mindepth 1 -maxdepth 1 -type f -print0)

  for signal in TERM INT KILL; do
    for pid in "${groups[@]}"; do
      actual="$(silo_process_starttime "$pid" 2>/dev/null || true)"
      [[ "$actual" == "$(<"$registry_dir/$pid")" ]] || { rm -f "$registry_dir/$pid"; continue; }
      kill "-$signal" -- "-$pid" 2>/dev/null || true
    done
    for _ in {1..20}; do
      local survivors=0
      for pid in "${groups[@]}"; do
        actual="$(silo_process_starttime "$pid" 2>/dev/null || true)"
        [[ -f "$registry_dir/$pid" ]] || continue
        expected="$(<"$registry_dir/$pid")"
        if [[ "$actual" == "$expected" ]]; then survivors=1; fi
      done
      [[ "$survivors" -eq 0 ]] && break
      sleep 0.1
    done
  done

  local failed=0
  for pid in "${groups[@]}"; do
    actual="$(silo_process_starttime "$pid" 2>/dev/null || true)"
    [[ -f "$registry_dir/$pid" ]] || continue
    expected="$(<"$registry_dir/$pid")"
    if [[ "$actual" == "$expected" ]]; then failed=1; else rm -f "$registry_dir/$pid"; fi
  done
  return "$failed"
}

silo_cleanup_status() {
  local status="$1"
  local registry_dir="$2"
  if cleanup_silo_test_process_groups "$registry_dir"; then
    return "$status"
  fi
  [[ "$status" -eq 0 ]] && return 1
  return "$status"
}
