#!/usr/bin/env bash
#
# convert_hls.sh — Convert HLS streams (.m3u8) to MP4 using ffmpeg.
#
# Reads the _export-manifest.json produced by graindl, finds all meetings
# with status "hls_pending", extracts the HLS URL from the corresponding
# .m3u8.url file, and converts each stream to MP4 via ffmpeg.
#
# Usage:
#   ./convert_hls.sh [OPTIONS] [OUTPUT_DIR]
#
# Arguments:
#   OUTPUT_DIR   Directory containing _export-manifest.json (default: ./recordings)
#
# Options:
#   -j, --jobs N     Max parallel conversions (default: 1)
#   -f, --force      Re-convert even if .mp4 already exists
#   -n, --dry-run    Show what would be converted without doing it
#   -q, --quiet      Suppress ffmpeg output
#   -h, --help       Show this help message
#
# Requirements:
#   - bash 4.3+ (uses mapfile and wait -n)
#   - ffmpeg (with HLS/TLS support)
#   - jq (for JSON parsing)
#
set -euo pipefail

# Match Go-side 0o600 permissions for all files created by this script.
umask 077

# ── Defaults ──────────────────────────────────────────────────────────────────

JOBS=1
FORCE=0
DRY_RUN=0
QUIET=0
OUTPUT_DIR=""

# ── Helpers ───────────────────────────────────────────────────────────────────

die()  { printf 'error: %s\n' "$1" >&2; exit 1; }
info() { printf '  %s\n' "$*"; }
warn() { printf '  WARN: %s\n' "$*" >&2; }

usage() {
    sed -n '2,/^$/{ s/^# \{0,1\}//; p; }' "$0"
    exit 0
}

check_deps() {
    command -v ffmpeg  >/dev/null 2>&1 || die "ffmpeg is required but not found in PATH"
    command -v ffprobe >/dev/null 2>&1 || die "ffprobe is required but not found in PATH (ships with ffmpeg)"
    command -v jq      >/dev/null 2>&1 || die "jq is required but not found in PATH"
}

# ── Argument Parsing ──────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
    case $1 in
        -j|--jobs)   JOBS="$2";  shift 2 ;;
        -f|--force)  FORCE=1;    shift   ;;
        -n|--dry-run) DRY_RUN=1; shift   ;;
        -q|--quiet)  QUIET=1;    shift   ;;
        -h|--help)   usage              ;;
        -*)          die "unknown option: $1" ;;
        *)           OUTPUT_DIR="$1"; shift ;;
    esac
done

OUTPUT_DIR="${OUTPUT_DIR:-./recordings}"
MANIFEST="${OUTPUT_DIR}/_export-manifest.json"

# ── Validation ────────────────────────────────────────────────────────────────

[[ "$JOBS" =~ ^[0-9]+$ && "$JOBS" -gt 0 ]] || die "--jobs must be a positive integer (got: ${JOBS})"

check_deps

[[ -f "$MANIFEST" ]] || die "manifest not found: ${MANIFEST}"

# ── Collect HLS-pending meetings ──────────────────────────────────────────────

# Extract meetings with status "hls_pending" as tab-separated: id \t video_path
mapfile -t PENDING < <(
    jq -r '.meetings[] | select(.status == "hls_pending") | [.id, .video_path] | @tsv' "$MANIFEST"
)

if [[ ${#PENDING[@]} -eq 0 ]]; then
    info "No HLS-pending meetings found in manifest."
    exit 0
fi

info "Found ${#PENDING[@]} HLS-pending meeting(s)"
echo

# ── Conversion ────────────────────────────────────────────────────────────────

CONVERTED=0
FAILED=0
SKIPPED=0
CONVERTED_IDS=()
PIDS=()
RESULTS_DIR=$(mktemp -d)
MANIFEST_TMP=""

cleanup() {
    [[ -n "$RESULTS_DIR" && -d "$RESULTS_DIR" ]] && rm -rf "$RESULTS_DIR"
    [[ -n "$MANIFEST_TMP" && -f "$MANIFEST_TMP" ]] && rm -f "$MANIFEST_TMP"
}
trap cleanup EXIT

convert_one() {
    local id="$1" url_file="$2" result_file="$3"

    # The video_path in the manifest is the .m3u8.url file (relative to output dir).
    local abs_url_file="${OUTPUT_DIR}/${url_file}"

    if [[ ! -f "$abs_url_file" ]]; then
        warn "[${id}] URL file not found: ${abs_url_file}"
        echo "failed" > "$result_file"
        return
    fi

    local hls_url
    hls_url=$(< "$abs_url_file")

    if [[ -z "$hls_url" ]]; then
        warn "[${id}] Empty URL in ${abs_url_file}"
        echo "failed" > "$result_file"
        return
    fi

    # Derive MP4 output path: foo.m3u8.url -> foo.mp4
    local mp4_path="${abs_url_file%.m3u8.url}.mp4"

    if [[ -f "$mp4_path" && "$FORCE" -eq 0 ]]; then
        info "[${id}] Already exists: ${mp4_path} (use --force to re-convert)"
        echo "skipped" > "$result_file"
        return
    fi

    if [[ "$DRY_RUN" -eq 1 ]]; then
        info "[${id}] Would convert: ${hls_url}"
        info "        -> ${mp4_path}"
        echo "skipped" > "$result_file"
        return
    fi

    info "[${id}] Converting HLS -> MP4 ..."

    local log_level="warning"
    if [[ "$QUIET" -eq 1 ]]; then
        log_level="error"
    fi

    # Probe whether the stream contains AAC audio; apply the ADTS-to-ASC
    # bitstream filter only when needed (it errors on non-AAC streams).
    local bsf_args=()
    if ffprobe -loglevel error -select_streams a:0 -show_entries stream=codec_name \
         -of csv=p=0 "$hls_url" 2>/dev/null | grep -q '^aac$'; then
        bsf_args=(-bsf:a aac_adtstoasc)
    fi

    local ffmpeg_args=(
        -y
        -i "$hls_url"
        -c copy
        -movflags +faststart
        "${bsf_args[@]}"
    )

    local rc=0
    ffmpeg -loglevel "$log_level" "${ffmpeg_args[@]}" "$mp4_path" </dev/null || rc=$?

    if [[ "$rc" -eq 0 ]]; then
        info "[${id}] OK: ${mp4_path}"
        echo "converted" > "$result_file"
    else
        warn "[${id}] ffmpeg failed (exit ${rc})"
        # Clean up partial file
        rm -f "$mp4_path"
        echo "failed" > "$result_file"
    fi
}

run_parallel() {
    local running=0

    for entry in "${PENDING[@]}"; do
        local id url_file
        id=$(cut -f1 <<< "$entry")
        url_file=$(cut -f2 <<< "$entry")

        local result_file="${RESULTS_DIR}/${id}"

        convert_one "$id" "$url_file" "$result_file" &
        PIDS+=($!)
        running=$((running + 1))

        # Throttle to max JOBS
        if [[ "$running" -ge "$JOBS" ]]; then
            wait -n 2>/dev/null || true
            running=$((running - 1))
        fi
    done

    # Wait for remaining
    for pid in "${PIDS[@]}"; do
        wait "$pid" 2>/dev/null || true
    done
}

run_sequential() {
    for entry in "${PENDING[@]}"; do
        local id url_file
        id=$(cut -f1 <<< "$entry")
        url_file=$(cut -f2 <<< "$entry")

        local result_file="${RESULTS_DIR}/${id}"
        convert_one "$id" "$url_file" "$result_file"
    done
}

if [[ "$JOBS" -gt 1 ]]; then
    run_parallel
else
    run_sequential
fi

# ── Tally results ────────────────────────────────────────────────────────────

for entry in "${PENDING[@]}"; do
    entry_id=$(cut -f1 <<< "$entry")
    result_file="${RESULTS_DIR}/${entry_id}"
    if [[ -f "$result_file" ]]; then
        case $(< "$result_file") in
            converted)
                CONVERTED=$((CONVERTED + 1))
                CONVERTED_IDS+=("$entry_id")
                ;;
            failed)  FAILED=$((FAILED + 1))  ;;
            skipped) SKIPPED=$((SKIPPED + 1)) ;;
        esac
    else
        FAILED=$((FAILED + 1))
    fi
done

# ── Update manifest ──────────────────────────────────────────────────────────

if [[ "$CONVERTED" -gt 0 && "$DRY_RUN" -eq 0 ]]; then
    # Build a JSON array of successfully converted IDs to pass to jq.
    IDS_JSON=$(printf '%s\n' "${CONVERTED_IDS[@]}" | jq -R . | jq -s .)

    # Update only the meetings whose IDs are in the converted list:
    #   - status "hls_pending" -> "ok"
    #   - video_path .m3u8.url -> .mp4
    #   - decrement hls_pending counter
    MANIFEST_TMP="${MANIFEST}.tmp"
    jq --argjson ids "$IDS_JSON" '
        ($ids | map({(.): true}) | add) as $set |
        .hls_pending = ([.hls_pending - ($ids | length), 0] | max) |
        .meetings = [.meetings[] |
            if .status == "hls_pending" and $set[.id] then
                .status = "ok" |
                .video_path = (.video_path | sub("\\.m3u8\\.url$"; ".mp4"))
            else . end
        ]
    ' "$MANIFEST" > "$MANIFEST_TMP" && mv "$MANIFEST_TMP" "$MANIFEST"
    MANIFEST_TMP=""  # clear so trap doesn't remove the renamed file

    info "Updated manifest: ${MANIFEST}"
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo
info "HLS conversion complete."
info "  Converted: ${CONVERTED}"
[[ "$SKIPPED" -gt 0 ]] && info "  Skipped:   ${SKIPPED}"
[[ "$FAILED"  -gt 0 ]] && info "  Failed:    ${FAILED}"

if [[ "$FAILED" -gt 0 ]]; then
    exit 1
fi
