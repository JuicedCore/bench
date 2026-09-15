#!/usr/bin/env bash
# shellcheck shell=bash
# Helpers shared by scripts/run-all.sh and scripts/gcp-run.sh for laying out a
# campaign directory and recording per-step results to a single summary. Source
# this; it changes no shell options and defines no side effects on its own.
#
#   . scripts/campaign-lib.sh
#   d="$(step_dir "$CAMPAIGN_DIR" "$platform" "$config")"
#   record "$CAMPAIGN_DIR" "$platform" "$config" deploy ok "" "$d"
#   print_summary "$CAMPAIGN_DIR"
#
# Status values used by callers: ok, no-measurement, deploy-failed, run-failed,
# container-failed. Stages: deploy, run.
#   no-measurement: benchrunner exited 0 but logged "FAILED RUN" (the headline
#   phase committed nothing, or the accounting invariant broke) - the result
#   files exist but are not a throughput number.

# step_dir <campaign-dir> <platform> <config-path> - per-(platform,config)
# results directory, created (with its capture/ subdir) if missing. Echoes
# the path.
step_dir() {
  local campaign_dir="$1" platform="$2" config="$3" d
  d="$campaign_dir/$platform/$(basename "$config" .yaml)"
  mkdir -p "$d/capture"
  echo "$d"
}

# reason_of <log-file> - one-line, tab-free, <=300 char failure reason pulled
# from a deploy/run log. In order of preference:
#   1. benchrunner's final "error: ..." line
#   2. the last "ERROR:" line from scripts/log-lib.sh (die) or level=ERROR from
#      benchrunner's structured log
#   3. benchrunner's "FAILED RUN" warning (exit 0, but not a measurement)
#   4. the last WARN line
#   5. the last non-empty line, so something is always shown
# Prints "no log" if the file doesn't exist.
reason_of() {
  local log="$1" clean line pat
  if [ ! -f "$log" ]; then
    echo "no log"
    return
  fi
  clean="$(sed -E 's/\x1b\[[0-9;]*m//g' "$log" 2>/dev/null || true)"

  line=""
  for pat in '^error:' 'ERROR:|level=ERROR' 'FAILED RUN' 'WARN:|level=WARN' '^!!'; do
    line="$(printf '%s\n' "$clean" | grep -E "$pat" | tail -1 || true)"
    [ -n "$line" ] && break
  done
  if [ -z "$line" ]; then
    line="$(printf '%s\n' "$clean" | grep -v '^[[:space:]]*$' | tail -1 || true)"
  fi
  # Drop slog's timestamp prefix; the level and message are what matter.
  line="$(printf '%s' "$line" | sed -E 's/^time=[^ ]+ //')"

  line="${line//$'\t'/ }"
  printf '%.300s\n' "$line"
}

# record <campaign-dir> <platform> <config-path> <stage> <status> <reason> <dir>
# Append one TSV row to <campaign-dir>/SUMMARY.tsv, writing the header first if
# the file doesn't exist yet. config is stored as its basename without .yaml;
# dir is stored relative to campaign-dir when it lives underneath it.
record() {
  local campaign_dir="$1" platform="$2" config="$3" stage="$4" status="$5" reason="$6" dir="$7"
  local summary="$campaign_dir/SUMMARY.tsv" cfg_base rel_dir

  cfg_base="$(basename "$config" .yaml)"
  case "$dir" in
    "$campaign_dir"/*) rel_dir="${dir#"$campaign_dir"/}" ;;
    *)                 rel_dir="$dir" ;;
  esac

  # A TSV row must be exactly one line with no embedded tabs.
  reason="${reason//$'\t'/ }"
  reason="${reason//$'\n'/ }"

  [ -f "$summary" ] || printf 'platform\tconfig\tstage\tstatus\treason\tdir\n' > "$summary"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$platform" "$cfg_base" "$stage" "$status" "$reason" "$rel_dir" \
    >> "$summary"
}

# print_summary <campaign-dir> - render SUMMARY.tsv as a table, if any rows
# were recorded, plus a pointer to the per-step logs and captures.
print_summary() {
  local campaign_dir="$1"
  local summary="$campaign_dir/SUMMARY.tsv"
  [ -f "$summary" ] || return 0

  echo "==================== summary ===================="
  if command -v column >/dev/null 2>&1; then
    column -t -s $'\t' < "$summary"
  else
    cat "$summary"
  fi
  echo "logs: $campaign_dir/<platform>/<config>/{deploy,run,teardown}.log, capture/"
  echo "run details: results/<platform>/<timestamp>/{summary.txt,run.log,error.txt}"
  echo "what each status means and how to triage: docs/README.md#troubleshooting"
}
