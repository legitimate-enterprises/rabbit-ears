#!/usr/bin/env bash
# Asserts the fixture set has the streams integration tests rely on.
set -euo pipefail
cd "$(dirname "$0")/../fixtures"

fail() { echo "fixtures: $*" >&2; exit 1; }
# probe <file> <stream selector> <entries>  -> comma-joined values
probe() { ffprobe -v error -select_streams "$2" -show_entries "$3" -of csv=p=0 "$1" | tr '\n' ' ' | sed 's/ *$//'; }
expect() { [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"; }

for f in hdr10-main10.mkv h264-high41.mp4 chapters.mkv sample.flac sample.mp3 sample.srt; do
    [ -s "$f" ] || fail "missing $f"
done

expect "$(probe hdr10-main10.mkv v stream=codec_name,profile,pix_fmt,color_transfer,color_primaries)" \
    "hevc,Main 10,yuv420p10le,smpte2084,bt2020" "hdr10 video"
expect "$(probe hdr10-main10.mkv a stream=codec_name)" "truehd aac" "hdr10 audio"
expect "$(probe hdr10-main10.mkv s stream=codec_name)" "hdmv_pgs_subtitle subrip" "hdr10 subtitles"
# Not `grep -q`: its early exit SIGPIPEs ffprobe under pipefail.
[ "$(ffprobe -v error -show_frames -read_intervals "%+#1" -select_streams v -show_entries frame=side_data_list \
    hdr10-main10.mkv | grep -c "Mastering display")" -ge 1 ] || fail "hdr10 mastering display metadata missing"
expect "$(probe h264-high41.mp4 v stream=codec_name,profile,level)" "h264,High,41" "h264 video"
expect "$(probe h264-high41.mp4 a stream=codec_name)" "aac" "h264 audio"
expect "$(ffprobe -v error -show_entries chapter=id -of csv=p=0 chapters.mkv | wc -l | tr -d ' ')" "3" "chapter count"
expect "$(probe sample.flac a stream=codec_name)" "flac" "flac"
expect "$(probe sample.mp3 a stream=codec_name)" "mp3" "mp3"
toy="library/Movies/Toy Story (1995)/Toy Story (1995).mp4"
guild="library/TV/The Guild/Season 01/The Guild - S01E01.avi"
expect "$(probe "$toy" v stream=codec_name)" "h264" "toy story video"
expect "$(probe "$toy" a stream=codec_name)" "aac" "toy story audio"
# The resume spec expects a duration within half a second of 51.1.
ffprobe -v error -show_entries format=duration -of csv=p=0 "$toy" | awk '{ exit !($1 > 50.6 && $1 < 51.6) }' \
    || fail "toy story duration not 51.1s"
expect "$(probe "$guild" v stream=codec_name)" "mpeg4" "guild video"
expect "$(probe "$guild" a stream=codec_name)" "mp3" "guild audio"
album="music/The Example Band/First Light (2024)"
loose="music/Folder Artist/Folder Album (2019)"
expect "$(probe "$album/01 Opening.flac" a stream=codec_name,channels)" "flac,2" "music flac"
expect "$(ffprobe -v error -show_entries stream_disposition=attached_pic -of csv=p=0 "$album/01 Opening.flac" | tr '\n' ' ' | sed 's/ *$//')" \
    "0 1" "music flac cover"
expect "$(ffprobe -v error -show_entries format_tags=artist,album_artist -of csv=p=0 "$album/02 Second Wind.mp3")" \
    "Guest Singer,The Example Band" "music mp3 tags"
expect "$(probe "$album/03 Last Call.m4a" a stream=codec_name)" "aac" "music m4a"
expect "$(ffprobe -v error -show_entries format_tags=title -of csv=p=0 "$loose/01 - Untagged Song.flac")" "" "untagged flac"
[ -s "$loose/cover.jpg" ] || fail "missing folder cover"
bench="youtube/Workbench Weekly"
first="$bench/Season 2019/s2019e080200 - Building a Bench"
second="$bench/Season 2020/s2020e030100 - Sharpening Chisels"
gears="youtube/Clockwork Corner/Gear Trains"
for f in "$first.mp4" "$second.mp4" "$gears.mp4"; do
    expect "$(probe "$f" v stream=codec_name)" "h264" "youtube video $f"
done
expect "$(ffprobe -v error -show_entries chapter=id -of csv=p=0 "$first.mp4" | wc -l | tr -d ' ')" "0" "youtube chapters only in info.json"
expect "$(ffprobe -v error -show_entries chapter=id -of csv=p=0 "$second.mp4" | wc -l | tr -d ' ')" "2" "youtube embedded chapters"
for f in "$bench/poster.jpg" "$bench/tvshow.nfo" "$first.nfo" "$first.info.json" "$first-thumb.jpg" "$first.en.srt" \
    "$second.nfo" "$second.info.json" "youtube/Clockwork Corner/tvshow.nfo" \
    "$bench/Season 2020/s2020e040100 - Unfinished.mp4.part" "$bench/Season 2020/s2020e050100 - Audio Only.m4a"; do
    [ -s "$f" ] || fail "missing $f"
done
echo "fixtures ok"
