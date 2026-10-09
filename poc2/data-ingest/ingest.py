#!/usr/bin/env python3
"""Load the engine-test CSVs into PostgreSQL (idempotent).

Files are looked up in DATA_DIR; if DATA_URL (SharePoint share link) is set,
missing/changed files are downloaded there first. Connection settings come
from the standard PG* environment variables.

Schema:
  runs(run_id)                              one row per test run (file prefix)
  run_metadata(run_id, parameter, value)    *_metadata.csv
  timeseries(run_id, source, signal, t, value)
      one row per sample; "short" files are cycle-resolved, "long" files are
      high-rate. Column Data1_<x> is paired with its Data1_time_<x> column.
"""
import csv
import http.cookiejar
import json
import os
import re
import sys
import urllib.parse
import urllib.request
from pathlib import Path

import psycopg

DATA_DIR = Path(os.environ.get("DATA_DIR", "/data"))
FILE_RE = re.compile(r"^(?P<run>.+?)_(?P<kind>metadata|timeseries_short|timeseries_long_\d+)\.csv$")
TIME_PREFIX = "Data1_time_"

SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (run_id text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS run_metadata (
    run_id text NOT NULL REFERENCES runs, parameter text NOT NULL, value text,
    PRIMARY KEY (run_id, parameter));
CREATE TABLE IF NOT EXISTS timeseries (
    run_id text NOT NULL REFERENCES runs, source text NOT NULL,
    signal text NOT NULL, t double precision NOT NULL, value double precision);
CREATE TABLE IF NOT EXISTS ingested_files (
    name text PRIMARY KEY, size bigint NOT NULL, loaded_at timestamptz NOT NULL DEFAULT now());
"""
INDEX = "CREATE INDEX IF NOT EXISTS timeseries_idx ON timeseries (run_id, signal, t)"


def download(url):
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    final = opener.open(url).geturl()
    parsed = urllib.parse.urlparse(final)
    folder = urllib.parse.parse_qs(parsed.query)["id"][0]
    base = f"{parsed.scheme}://{parsed.netloc}{'/'.join(folder.split('/')[:3])}/_api/web"
    req = urllib.request.Request(
        f"{base}/GetFolderByServerRelativeUrl('{urllib.parse.quote(folder, safe='')}')/Files"
        "?$select=Name,ServerRelativeUrl,Length",
        headers={"Accept": "application/json;odata=nometadata"})
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    for f in json.load(opener.open(req))["value"]:
        target = DATA_DIR / f["Name"]
        if not f["Name"].lower().endswith(".csv") or (
                target.exists() and target.stat().st_size == int(f["Length"])):
            continue
        print(f"get {f['Name']}", flush=True)
        rel = urllib.parse.quote(f["ServerRelativeUrl"], safe="")
        part = target.with_suffix(".part")
        with opener.open(f"{base}/GetFileByServerRelativeUrl('{rel}')/$value") as r, open(part, "wb") as out:
            while chunk := r.read(1 << 20):
                out.write(chunk)
        part.rename(target)


def load_metadata(cur, run, path):
    with path.open(newline="") as fh, cur.copy(
            "COPY run_metadata (run_id, parameter, value) FROM STDIN") as cp:
        rows = csv.reader(fh)
        next(rows)
        for row in rows:
            if row:
                cp.write_row((run, row[0], row[1] if len(row) > 1 and row[1] != "" else None))


def load_timeseries(cur, run, source, path):
    with path.open(newline="") as fh, cur.copy(
            "COPY timeseries (run_id, source, signal, t, value) FROM STDIN") as cp:
        rows = csv.reader(fh)
        header = next(rows)
        idx = {h: i for i, h in enumerate(header)}
        pairs = [(h[len("Data1_"):], i, idx[TIME_PREFIX + h[len("Data1_"):]])
                 for h, i in idx.items()
                 if h.startswith("Data1_") and not h.startswith(TIME_PREFIX)
                 and TIME_PREFIX + h[len("Data1_"):] in idx]
        for row in rows:
            for signal, vi, ti in pairs:
                cp.write_row((run, source, signal, row[ti], row[vi] or None))


def main():
    if url := os.environ.get("DATA_URL"):
        download(url)
    files = sorted(p for p in DATA_DIR.glob("*.csv") if FILE_RE.match(p.name))
    if not files:
        sys.exit(f"no CSV files found in {DATA_DIR}")

    with psycopg.connect(autocommit=True) as conn:
        conn.execute(SCHEMA)
        done = dict(conn.execute("SELECT name, size FROM ingested_files").fetchall())
        for path in files:
            size = path.stat().st_size
            if done.get(path.name) == size:
                print(f"skip {path.name}", flush=True)
                continue
            m = FILE_RE.match(path.name)
            run, kind = m["run"], m["kind"]
            print(f"load {path.name}", flush=True)
            with conn.transaction(), conn.cursor() as cur:
                cur.execute("INSERT INTO runs VALUES (%s) ON CONFLICT DO NOTHING", (run,))
                if kind == "metadata":
                    cur.execute("DELETE FROM run_metadata WHERE run_id = %s", (run,))
                    load_metadata(cur, run, path)
                else:
                    source = kind.replace("timeseries_", "")
                    cur.execute("DELETE FROM timeseries WHERE run_id = %s AND source = %s", (run, source))
                    load_timeseries(cur, run, source, path)
                cur.execute(
                    "INSERT INTO ingested_files (name, size) VALUES (%s, %s) "
                    "ON CONFLICT (name) DO UPDATE SET size = EXCLUDED.size, loaded_at = now()",
                    (path.name, size))
        conn.execute(INDEX)
        conn.execute("ANALYZE timeseries")
    print("done", flush=True)


if __name__ == "__main__":
    main()
