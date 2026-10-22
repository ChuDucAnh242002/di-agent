#!/bin/sh
# Minimal exec collector: one JSON line in, one JSON line out.
while IFS= read -r line; do
  case "$line" in
    *'"op":"hello"'*) echo '{"protocol":1}' ;;
    *'"op":"collect"'*)
      seq=$(printf '%s' "$line" | sed 's/.*"seq":\([0-9]*\).*/\1/')
      now=$(printf '%s' "$line" | sed 's/.*"now_unix":\([0-9]*\).*/\1/')
      v=$(cut -d' ' -f1 /proc/loadavg)
      printf '{"seq":%s,"samples":[{"metric_type":"load1","value":%s,"timestamp_unix":%s,"unit":"load"}]}\n' "$seq" "$v" "$now" ;;
  esac
done
