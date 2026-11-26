# Rabbit Ears
A self-hosted open-source media server for movies, TV, music and YouTube videos. 

------------------------------------------------------------------------
## What?
A simplified & opinionated all-in-one alternative for Plex, Navidrome, ErsatzTV,
and TubeArchivist with native apps for many screen types.
- Libraries: Movies, Shows, Music, YouTube
- Clients for: web, iOS, tvOS, macOS, Android, Roku
- Playback: Direct play, remuxing, audio/video transcoding
- Smart playlists: rules-based dynamic playlists
- Households: Manage access & visibility for friends & family
- Kid accounts: Control age-appropriate content visibility
- Stations: Easy-to-configure streaming channels; no more analysis paralysis
- Occasions: Seasonal movies and shows on Home while their holiday is near
- Watch Together: Synchronized playback with someone else
- Downloads: For offline consumption

------------------------------------------------------------------------
## Why?
I don't like surveillance capitalism. I like privacy, data sovereignty, and
convenience, which don't often go together.

------------------------------------------------------------------------
## Building
- Server: macOS on Apple silicon or Linux, Go, and `ffmpeg` with libx264
  and libx265 and `ffprobe`; on Linux also `pkill` (procps).
- Fixtures, which the integration and end-to-end tests use: `python3` and
  `ffmpeg` with libmp3lame.
- Web client, and the server's `make build`, which embeds it: Node LTS. Its
  end-to-end tests: Google Chrome.
- `make deps` installs the rest.
- Each of these at its latest stable release; an older one, such as the
  ffmpeg 6 some distributions ship, is not supported.
- `make release` tags a version once CI is green; the tag publishes the
  release.

------------------------------------------------------------------------
## Repository
- `server/` Go server and OpenAPI spec.
- `web/` TypeScript web client (distributed with the server).
- `scripts/` internal & dev tooling.
- `fixtures/` CC media for integration tests.

The other clients have repositories of their own, each built against one
server release:
- `rabbit-ears-apple` macOS, iOS and Apple TV clients.
- `rabbit-ears-android` Android client.
- `rabbit-ears-roku` Roku client.


------------------------------------------------------------------------
*End of README*
