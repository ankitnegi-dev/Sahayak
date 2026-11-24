#!/usr/bin/env python3
"""
Common Voice -> Sahayak ASR benchmark corpus converter.

This is tooling, not product code. The roadmap explicitly keeps a small Python
script for ASR benchmark work (jiwer for WER). Everything shipped to users
stays in Go.

What it does
------------
Takes a slice of Mozilla Common Voice, filters for clean clips, and emits a
manifest.jsonl in the format cmd/asrbench expects, copying the audio files
alongside it.

Common Voice ships audio as MP3. WhatsApp sends OGG/Opus, so this script
converts each clip to OGG/Opus with ffmpeg when it is available, falling back
to MP3 otherwise. The benchmark harness detects the format from the extension.

Usage
-----
    python scripts/cv_to_manifest.py \
        --clips /path/to/common_voice/clips \
        --tsv /path/to/common_voice/test.tsv \
        --out corpus \
        --limit 50

The TSV must have at least the columns: path, sentence, up_votes, down_votes.
Common Voice's standard export has these.

Filtering
---------
By default only clips with up_votes >= 2 and down_votes == 0 are kept, which
removes the obviously mislabelled ones. You should still listen to a sample of
the output and fix any transcripts that are wrong, because Common Voice
transcripts have spelling inconsistencies that would corrupt the benchmark.

Output layout
------------
    corpus/
      manifest.jsonl
      s0001.ogg
      s0002.ogg
      ...
"""

import argparse
import csv
import json
import os
import random
import shutil
import subprocess
import sys


def find_ffmpeg():
    return shutil.which("ffmpeg")


def convert_to_ogg(src, dst):
    """Convert an audio file to OGG/Opus using ffmpeg. Returns True on success."""
    ffmpeg = find_ffmpeg()
    if ffmpeg is None:
        return False
    result = subprocess.run(
        [ffmpeg, "-hide_banner", "-loglevel", "error",
         "-i", src, "-c:a", "libopus", "-b:a", "24k", "-y", dst],
        capture_output=True,
    )
    return result.returncode == 0 and os.path.exists(dst) and os.path.getsize(dst) > 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--clips", required=True, help="directory containing the Common Voice audio files")
    parser.add_argument("--tsv", required=True, help="path to the Common Voice TSV (test.tsv or dev.tsv)")
    parser.add_argument("--out", required=True, help="output directory for the corpus")
    parser.add_argument("--limit", type=int, default=50, help="maximum number of clips to include")
    parser.add_argument("--min-up-votes", type=int, default=2, help="minimum up_votes to keep a clip")
    parser.add_argument("--seed", type=int, default=42, help="random seed for reproducible selection")
    parser.add_argument("--no-convert", action="store_true", help="keep MP3 instead of converting to OGG/Opus")
    args = parser.parse_args()

    if not os.path.isdir(args.clips):
        print(f"error: clips directory not found: {args.clips}", file=sys.stderr)
        sys.exit(1)
    if not os.path.isfile(args.tsv):
        print(f"error: TSV not found: {args.tsv}", file=sys.stderr)
        sys.exit(1)

    os.makedirs(args.out, exist_ok=True)

    # Read and filter the TSV.
    rows = []
    with open(args.tsv, newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f, delimiter="\t")
        for row in reader:
            try:
                up = int(row.get("up_votes", "0") or 0)
                down = int(row.get("down_votes", "0") or 0)
            except ValueError:
                continue
            if up < args.min_up_votes or down != 0:
                continue
            sentence = (row.get("sentence") or "").strip()
            if not sentence:
                continue
            path = row.get("path") or ""
            if not path:
                continue
            rows.append((path, sentence))

    if not rows:
        print("error: no clips survived filtering; try lowering --min-up-votes", file=sys.stderr)
        sys.exit(1)

    random.seed(args.seed)
    random.shuffle(rows)
    rows = rows[: min(args.limit, len(rows))]

    ffmpeg_available = find_ffmpeg() is not None
    if not args.no_convert and not ffmpeg_available:
        print("warning: ffmpeg not found on PATH; keeping MP3 files", file=sys.stderr)
        args.no_convert = True

    manifest_lines = []
    copied = 0
    for i, (rel_path, sentence) in enumerate(rows, start=1):
        src = os.path.join(args.clips, rel_path)
        if not os.path.isfile(src):
            print(f"warning: skipping missing file {src}", file=sys.stderr)
            continue

        sample_id = f"s{i:04d}"
        if args.no_convert:
            ext = os.path.splitext(rel_path)[1] or ".mp3"
            dst_name = sample_id + ext
        else:
            dst_name = sample_id + ".ogg"

        dst = os.path.join(args.out, dst_name)
        if args.no_convert:
            shutil.copyfile(src, dst)
        else:
            if not convert_to_ogg(src, dst):
                print(f"warning: conversion failed for {src}; keeping MP3", file=sys.stderr)
                dst_name = sample_id + (os.path.splitext(rel_path)[1] or ".mp3")
                dst = os.path.join(args.out, dst_name)
                shutil.copyfile(src, dst)

        mime_type = "audio/ogg; codecs=opus" if dst_name.endswith(".ogg") else "audio/mpeg"

        entry = {
            "id": sample_id,
            "path": dst_name,
            "reference": sentence,
            "mime_type": mime_type,
            "tags": ["common-voice", "clean"],
        }
        manifest_lines.append(json.dumps(entry, ensure_ascii=False))
        copied += 1

    manifest_path = os.path.join(args.out, "manifest.jsonl")
    with open(manifest_path, "w", encoding="utf-8") as f:
        f.write("\n".join(manifest_lines) + "\n")

    print(f"wrote {copied} samples to {args.out}")
    print(f"manifest: {manifest_path}")
    if args.no_convert:
        print("audio kept as MP3; the benchmark harness detects the format from the extension")
    else:
        print("audio converted to OGG/Opus to match WhatsApp's wire format")
    print()
    print("NEXT STEPS:")
    print("  1. Listen to a sample of the clips and fix any wrong transcripts in manifest.jsonl")
    print("  2. Run: go run ./cmd/asrbench -manifest " + manifest_path + " -provider sarvam -provider openai -provider google")
    print("  3. Record 30 real voice notes from your parents and replace this corpus before making the final decision")


def min(a, b):
    return a if a < b else b


if __name__ == "__main__":
    main()
