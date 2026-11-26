#!/usr/bin/env bash
# Populates fixtures/ for integration tests; files are git-ignored.
#
# With FIXTURES_SOURCE set to a directory its files are copied. Otherwise
# they are synthesized deterministically with ffmpeg. Either way the set is
# verified with ffprobe, so a test can rely on every file below existing
# with the stated streams.
set -euo pipefail
cd "$(dirname "$0")/.."
out=fixtures
mkdir -p "$out"
here="$(pwd)"

NAMES="hdr10-main10.mkv h264-high41.mp4 chapters.mkv sample.flac sample.mp3 sample.srt"

if [ -n "${FIXTURES_SOURCE:-}" ]; then
    for f in $NAMES; do cp "$FIXTURES_SOURCE/$f" "$out/$f"; done
fi

missing=0
for f in $NAMES; do [ -s "$out/$f" ] || missing=1; done

if [ "$missing" = 1 ]; then
    command -v ffmpeg >/dev/null || { echo "ffmpeg is required to synthesize fixtures" >&2; exit 1; }
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    # bitexact + no metadata keeps the bytes stable across runs.
    # Output-side options must follow the inputs, so they are spliced in
    # before the last argument (the output path).
    ff() {
        local dest="${*: -1}"
        set -- "${@:1:$#-1}"
        ffmpeg -v error -y "$@" -fflags +bitexact -flags +bitexact -map_metadata -1 "$dest"
    }
    V_SRC=(-f lavfi -i "testsrc2=size=640x360:rate=24:duration=10")
    A_SRC=(-f lavfi -i "sine=frequency=440:sample_rate=48000:duration=10")

    cat > "$tmp/sample.srt" <<'SRT'
1
00:00:00,500 --> 00:00:02,000
Hello, forced.

2
00:00:03,000 --> 00:00:05,000
Second line.
SRT
    python3 -I scripts/make-pgs.py "$tmp/sample.sup"

    # HEVC Main 10 with HDR10 static metadata; TrueHD (experimental encoder
    # needs -strict -2); AAC; PGS (stream copy); SRT.
    ff "${V_SRC[@]}" "${A_SRC[@]}" "${A_SRC[@]}" -i "$tmp/sample.sup" -i "$tmp/sample.srt" \
        -map 0:v -map 1:a -map 2:a -map 3:s -map 4:s \
        -c:v libx265 -pix_fmt yuv420p10le -preset ultrafast -b:v 400k -tag:v hvc1 \
        -x265-params "log-level=error:repeat-headers=1:hdr10=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400" \
        -c:a:0 truehd -strict -2 -ac 2 -c:a:1 aac -b:a:1 96k \
        -c:s:0 copy -c:s:1 srt \
        -metadata:s:a:0 language=eng -metadata:s:a:1 language=eng \
        -metadata:s:s:0 language=eng -metadata:s:s:1 language=eng \
        -disposition:s:0 +forced \
        "$out/hdr10-main10.mkv"

    ff "${V_SRC[@]}" "${A_SRC[@]}" -map 0:v -map 1:a \
        -c:v libx264 -profile:v high -level 4.1 -pix_fmt yuv420p -preset veryfast -b:v 400k \
        -c:a aac -b:a 96k -movflags +faststart "$out/h264-high41.mp4"

    cat > "$tmp/chapters.ffmeta" <<'META'
;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=3000
title=Opening
[CHAPTER]
TIMEBASE=1/1000
START=3000
END=7000
title=Middle
[CHAPTER]
TIMEBASE=1/1000
START=7000
END=10000
title=Ending
META
    ff "${V_SRC[@]}" "${A_SRC[@]}" -i "$tmp/chapters.ffmeta" -map 0:v -map 1:a -map_chapters 2 \
        -c:v libx264 -pix_fmt yuv420p -preset ultrafast -b:v 300k -c:a aac -b:a 96k "$out/chapters.mkv"

    ff "${A_SRC[@]}" -c:a flac -metadata title=Fixture -metadata artist=Rabbit "$out/sample.flac"
    ff "${A_SRC[@]}" -c:a libmp3lame -b:a 128k -metadata title=Fixture -metadata artist=Rabbit "$out/sample.mp3"
    cp "$tmp/sample.srt" "$out/sample.srt"
fi

# The e2e library: real rips may stand here; only absent files are
# synthesized. The specs need an H.264/AAC MP4 of 51.1s (resume, seek and
# duration checks) and an MPEG-4 Part 2/MP3 AVI.
toy="$out/library/Movies/Toy Story (1995)/Toy Story (1995).mp4"
guild="$out/library/TV/The Guild/Season 01/The Guild - S01E01.avi"
if [ ! -s "$toy" ] || [ ! -s "$guild" ]; then
    command -v ffmpeg >/dev/null || { echo "ffmpeg is required to synthesize fixtures" >&2; exit 1; }
    mkdir -p "$(dirname "$toy")" "$(dirname "$guild")"
    L_V=(-f lavfi -i "testsrc2=size=640x360:rate=24:duration=51.1")
    L_A=(-f lavfi -i "sine=frequency=440:sample_rate=48000:duration=51.1")
    lib() { ffmpeg -v error -y "$@" -fflags +bitexact -flags +bitexact -map_metadata -1; }
    [ -s "$toy" ] || lib "${L_V[@]}" "${L_A[@]}" -map 0:v -map 1:a \
        -c:v libx264 -pix_fmt yuv420p -preset veryfast -g 48 -b:v 400k \
        -c:a aac -b:a 96k -movflags +faststart "$toy"
    [ -s "$guild" ] || lib -f lavfi -i "testsrc2=size=640x360:rate=24:duration=10" \
        -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=10" -map 0:v -map 1:a \
        -c:v mpeg4 -q:v 5 -c:a libmp3lame -b:a 128k "$guild"
fi

# The music library: tagged songs of one album (FLAC with an embedded cover,
# MP3 by a guest performer, AAC in M4A without an album artist tag) and one
# untagged FLAC that only its folders name, beside a folder cover.
album="$out/music/The Example Band/First Light (2024)"
loose="$out/music/Folder Artist/Folder Album (2019)"
if [ ! -s "$album/01 Opening.flac" ] || [ ! -s "$album/02 Second Wind.mp3" ] || [ ! -s "$album/03 Last Call.m4a" ] \
    || [ ! -s "$loose/01 - Untagged Song.flac" ] || [ ! -s "$loose/cover.jpg" ]; then
    command -v ffmpeg >/dev/null || { echo "ffmpeg is required to synthesize fixtures" >&2; exit 1; }
    mkdir -p "$album" "$loose"
    M_A=(-f lavfi -i "sine=frequency=440:sample_rate=44100:duration=3")
    song() { ffmpeg -v error -y "$@" -fflags +bitexact -flags +bitexact -map_metadata -1; }
    song -f lavfi -i "color=c=0x3366cc:s=300x300" -frames:v 1 "$loose/cover.jpg"
    song "${M_A[@]}" -i "$loose/cover.jpg" -map 0:a -map 1:v -ac 2 -c:a flac -c:v copy -disposition:v attached_pic \
        -metadata title=Opening -metadata artist="The Example Band" -metadata album_artist="The Example Band" \
        -metadata album="First Light" -metadata track=1/3 -metadata disc=1/1 -metadata date=2024 -metadata genre=Rock \
        "$album/01 Opening.flac"
    song "${M_A[@]}" -ac 2 -c:a libmp3lame -b:a 128k \
        -metadata title="Second Wind" -metadata artist="Guest Singer" -metadata album_artist="The Example Band" \
        -metadata album="First Light" -metadata track=2/3 -metadata disc=1/1 -metadata date=2024 -metadata genre=Rock \
        "$album/02 Second Wind.mp3"
    song "${M_A[@]}" -ac 2 -c:a aac -b:a 128k \
        -metadata title="Last Call" -metadata artist="The Example Band" -metadata album="First Light" \
        -metadata track=3 -metadata date=2024-06-01 "$album/03 Last Call.m4a"
    song "${M_A[@]}" -ac 2 -c:a flac "$loose/01 - Untagged Song.flac"
fi

# The YouTube library in the layout Pinchflat writes. A channel folder with
# tvshow.nfo and poster.jpg holds season folders of videos with .nfo,
# .info.json, -thumb.jpg and .en.srt sidecars, beside download strays. The
# first video's chapters are only in its .info.json, with a SponsorBlock
# segment; the second has embedded chapters, an .nfo with only an air date
# and an .info.json whose chapters lose to the embedded ones. A second
# channel follows a playlist and holds one bare video outside any season.
bench="$out/youtube/Workbench Weekly"
first="$bench/Season 2019/s2019e080200 - Building a Bench"
second="$bench/Season 2020/s2020e030100 - Sharpening Chisels"
gears="$out/youtube/Clockwork Corner/Gear Trains"
if [ ! -s "$first.mp4" ] || [ ! -s "$second.mp4" ] || [ ! -s "$gears.mp4" ] || [ ! -s "$bench/poster.jpg" ] \
    || [ ! -s "$first-thumb.jpg" ]; then
    command -v ffmpeg >/dev/null || { echo "ffmpeg is required to synthesize fixtures" >&2; exit 1; }
    mkdir -p "$(dirname "$first")" "$(dirname "$second")" "$(dirname "$gears")"
    Y_V=(-f lavfi -i "testsrc2=size=320x180:rate=24:duration=3")
    Y_A=(-f lavfi -i "sine=frequency=440:sample_rate=48000:duration=3")
    yt() {
        local dest="${*: -1}"
        set -- "${@:1:$#-1}"
        ffmpeg -v error -y "$@" -fflags +bitexact -flags +bitexact "$dest"
    }
    clip() {
        local dest="${*: -1}"
        set -- "${@:1:$#-1}"
        yt "${Y_V[@]}" "${Y_A[@]}" "$@" -c:v libx264 -pix_fmt yuv420p -preset ultrafast -b:v 200k \
            -c:a aac -b:a 64k -movflags +faststart "$dest"
    }
    clip -map 0:v -map 1:a -map_metadata -1 -map_chapters -1 "$first.mp4"
    clip -map 0:v -map 1:a -map_metadata -1 -map_chapters -1 "$gears.mp4"
    meta="$(mktemp)"
    cat > "$meta" <<'META'
;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=1500
title=Grind
[CHAPTER]
TIMEBASE=1/1000
START=1500
END=3000
title=Hone
META
    clip -i "$meta" -map 0:v -map 1:a -map_chapters 2 "$second.mp4"
    rm -f "$meta"
    yt -f lavfi -i "color=c=0x8a5a2b:s=200x200" -frames:v 1 "$bench/poster.jpg"
    yt -f lavfi -i "color=c=0x2b5a8a:s=320x180" -frames:v 1 "$first-thumb.jpg"
    cat > "$bench/tvshow.nfo" <<'NFO'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
  <title>Workbench Weekly Uploads</title>
  <plot>Projects from a small shop.</plot>
  <uniqueid type="youtube" default="true">UCworkbenchweekly00000a</uniqueid>
</tvshow>
NFO
    cat > "$(dirname "$gears")/tvshow.nfo" <<'NFO'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
  <title>Clockwork Corner</title>
  <plot>Gears, one at a time.</plot>
  <uniqueid type="youtube" default="true">PLclockworkcorner00000b</uniqueid>
</tvshow>
NFO
    cat > "$first.nfo" <<'NFO'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<episodedetails>
  <title>Building a Bench</title>
  <showtitle>Workbench Weekly</showtitle>
  <uniqueid type="youtube" default="true">Wkbnch19801</uniqueid>
  <plot>A bench from one board.</plot>
  <aired>2019-08-02 00:00:00Z</aired>
</episodedetails>
NFO
    cat > "$first.info.json" <<'JSON'
{"id": "Wkbnch19801", "title": "Building a Bench (info)", "description": "From the info file.", "timestamp": 1564754400,
 "upload_date": "20190802", "duration": 3,
 "formats": [{"format_id": "18", "url": "https://example.invalid/18"}],
 "chapters": [{"start_time": 0.0, "end_time": 1.0, "title": "Intro"}, {"start_time": 1.0, "end_time": 3.0, "title": "Build"}],
 "sponsorblock_chapters": [{"start_time": 0.5, "end_time": 1.5, "category": "sponsor", "title": "Sponsor", "type": "skip"}]}
JSON
    cat > "$first.en.srt" <<'SRT'
1
00:00:00,500 --> 00:00:02,000
Measure twice.
SRT
    cat > "$second.nfo" <<'NFO'
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<episodedetails>
  <title>Sharpening Chisels</title>
  <showtitle>Workbench Weekly</showtitle>
  <uniqueid type="youtube" default="true">Wkbnch20301</uniqueid>
  <aired>2020-03-01 00:00:00Z</aired>
</episodedetails>
NFO
    cat > "$second.info.json" <<'JSON'
{"id": "Wkbnch20301", "title": "Sharpening Chisels (info)", "chapters": [{"start_time": 0, "end_time": 3, "title": "Whole"}]}
JSON
    strays="$bench/Season 2020"
    printf 'partial' > "$strays/s2020e040100 - Unfinished.mp4.part"
    printf '{}' > "$strays/s2020e040100 - Unfinished.mp4.ytdl"
    : > "$strays/.keep"
    printf 'finder' > "$bench/.DS_Store"
    yt -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=1" -c:a aac -b:a 64k -map_metadata -1 \
        "$strays/s2020e050100 - Audio Only.m4a"
fi

exec "$here/scripts/verify-fixtures.sh"
