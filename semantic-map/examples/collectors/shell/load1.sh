#!/bin/sh
# Minimal exec collector: one JSON line in, one JSON line out.
while IFS= read -r line; do
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"op":"hello"'*) echo '{"protocol":1}' ;;
    *'"op":"collect"'*)
      now=$(printf '%s' "$line" | sed 's/.*"now_unix":\([0-9]*\).*/\1/')
      v=$(cut -d' ' -f1 /proc/loadavg)
      printf '{"seq":%s,"samples":[{"metric_type":"load1_raw","value":%s,"timestamp_unix":%s,"unit":"load"}]}\n' "$seq" "$v" "$now" ;;
    *) printf '{"seq":%s,"error":"unknown op"}\n' "${seq:-0}" ;;
  esac
done
