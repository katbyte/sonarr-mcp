#!/usr/bin/env bash
#
# Lay out a throwaway TV library of tiny videos, and create (but do not start)
# a Sonarr container over it with a known API key, then print the environment
# the live test suites need.
#
#   eval "$(scripts/testenv.sh up)"   # create the container, export SONARR_*
#   scripts/testenv.sh down            # remove the container and the library
#   scripts/testenv.sh fixtures        # write the library only, to inspect it
#   scripts/testenv.sh logs            # what Sonarr wrote, for a failing run
#
# The suites start the container themselves (docker start), once their
# record/replay proxy is listening: Sonarr calls services.sonarr.tv as it
# starts, and a call made before the proxy is up is a call neither recorded
# nor replayed - so a recording made one way would miss on a replay made the
# other. Starting it from the suite makes every run see the same calls.
#
# This script only does what sonarr-mcp cannot: create the container, write
# its config (the API key) and lay the library out on disk. Adding the root
# folder, importing and adding the series, the indexer and the download
# client are the suites' job, through the tools, so those are exercised
# rather than bypassed.
#
# Sonarr is a .NET app: it honours HTTPS_PROXY and, on Linux, trusts whatever
# SSL_CERT_FILE names, so the proxy's certificate authority is minted here
# (openssl) and mounted in, and the suites load the same files to sign with.
# SkyHook (TheTVDB's front), services.sonarr.tv and the artwork CDN all go
# through it; the fake indexer and download client the suites run are on the
# host too, and reached directly (NO_PROXY).

set -euo pipefail

IMAGE="${SONARR_TEST_IMAGE:-lscr.io/linuxserver/sonarr:version-4.0.20.3014}"
NAME="${SONARR_TEST_CONTAINER:-sonarr-mcp-test}"
PORT="${SONARR_TEST_PORT:-18989}"
# the ports the suites' proxy, fake indexer and fake SABnzbd listen on, reached
# from inside the container on host.docker.internal
PROXY_PORT="${SONARR_TEST_PROXY_PORT:-18080}"
INDEXER_PORT="${SONARR_TEST_INDEXER_PORT:-18081}"
SAB_PORT="${SONARR_TEST_SAB_PORT:-18082}"
# not TMPDIR: on macOS that is /var/folders/..., which a Docker VM does not
# share by default, and the bind mounts silently come up empty
DATA="${SONARR_TEST_DATA:-${HOME}/.cache/sonarr-mcp/testenv/default}"
# the proxy's certificate authority, shared by every container so one set of
# cassettes serves them all
PROXY_CA="${SONARR_TEST_PROXY_CA:-${HOME}/.cache/sonarr-mcp/testenv/proxy}"
URL="http://127.0.0.1:${PORT}"

log() { echo "==> $*" >&2; }

# proxy_host is the address the container reaches the host on. Docker Desktop
# and Colima provide host.docker.internal; on Linux docker maps it with
# --add-host, and the container is handed the bridge gateway's address
# instead, so a runtime that prefers an IPv6 answer cannot pick a route the
# host does not listen on.
proxy_host() {
  if [ "$(uname -s)" = "Linux" ]; then
    docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null && return 0
  fi
  echo "host.docker.internal"
}

# ---------------------------------------------------------------------------
# The library. Real shows with their real TheTVDB ids, so Sonarr's own
# metadata (recorded through the proxy) and the audits have something true to
# compare against; the files are black frames of the episode's real running
# time, so the runtime audit has nothing to say about them unless a fixture
# means it to. Change a line here and the suites' fixture tables
# (acceptance/fixtures_test.go) must agree.
#
# Sonarr's default naming (NamingConfig.Default): series folders are the
# title, season folders "Season N", episodes
# "{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}",
# so a file laid out that way is one Sonarr would not rename.
#
# folder|season|episode|file name|minutes|frame size|audio language
# (an empty audio language is a track tagged und, undetermined)
FILES='Firefly|1|1|Firefly - S01E01 - The Train Job WEBDL-1080p|44|1920x1080|eng
Firefly|1|2|Firefly - S01E02 - Bushwhacked WEBDL-1080p|45|1920x1080|
Firefly|1|3|Firefly - S01E03 - Our Mrs. Reynolds WEBDL-1080p|45|1920x1080|
Firefly|1|4|Firefly - S01E04 - Jaynestown HDTV-1080p|45|640x360|
Firefly|1|5|Firefly - S01E05 - Out of Gas WEBDL-1080p|5|1920x1080|
Firefly|1|6|Firefly - S01E06 - Shindig SDTV|45|640x480|
Chernobyl (2019)|1|1|Chernobyl.S01E01.1.23.45.1080p.WEB-DL.DD5.1.H.264-NTb|59|1920x1080|
Chernobyl (2019)|1|2|Chernobyl.S01E02.Please.Remain.Calm.1080p.WEB-DL.DD5.1.H.264-NTb|65|1920x1080|
Chernobyl (2019)|1|3|Chernobyl.S01E03.Open.Wide.O.Earth.1080p.WEB-DL.DD5.1.H.264-NTb|62|1920x1080|
Chernobyl (2019)|1|4|Chernobyl.S01E04.The.Happiness.of.All.Mankind.1080p.WEB-DL.DD5.1.H.264-NTb|65|1920x1080|
Chernobyl (2019)|1|5|Chernobyl.S01E05.Vichnaya.Pamyat.GERMAN.1080p.WEB-DL.DD5.1.H.264-NTb|72|1920x1080|ger
Cowboy Bebop|1|1|Cowboy Bebop - S01E01 - Session #2 Stray Dog Strut Bluray-1080p|25|1920x1080|jpn
Cowboy Bebop|1|2|Cowboy Bebop - S01E02 - Session #3 Honky Tonk Women Bluray-1080p|25|1920x1080|jpn
Severance|1|1|Severance - S01E01 - Good News About Hell WEBDL-2160p|57|3840x2160|
Severance|1|2|Severance - S01E02 - Half Loop WEBDL-2160p|53|3840x2160|
The Expanse|1|1|The Expanse - S01E01 - Dulcinea WEBDL-1080p|44|1920x1080|
The Expanse|1|2|The Expanse - S01E02 - The Big Empty WEBDL-1080p|44|1920x1080|'

# What the library is for, series by series:
#
#   Firefly           imported; E01-E03 clean; E04 named HDTV-1080p but 360p, which Sonarr reads
#                     from the video and records as SDTV (a test then edits it to the name's
#                     quality, for audit_quality_mismatch); E05 runs 5 minutes of 45
#                     (audit_runtime); E06 SDTV; E04 and E06 below the HD-1080p cutoff
#                     (audit_cutoff_unmet); E07-E14 missing (audit_missing_episodes).
#                     The suite later drops in a file Sonarr has not seen and deletes E03 on disk
#                     (audit_untracked_files, audit_missing_files).
#   Chernobyl (2019)  imported; a folder not named to the format (audit_series_settings), scene
#                     names Sonarr would rename (audit_naming), E05 German with a German audio track
#                     (audit_language)
#   Cowboy Bebop      imported as a standard series, though it is anime (audit_series_settings)
#   Severance         imported; still airing, and set not to monitor new seasons (audit_monitoring)
#   The Expanse       left unmapped (audit_unmapped_folders), for series_import to take in
#   Breaking Bad      added with no files, the one the downloads are for: searches, grabs, the
#                     queue, imports, failures

# wipe_data removes the data directory. The container writes its config and
# database as its own user, and on Linux those land in the bind mount owned
# by an id the calling user cannot delete; a throwaway root container can.
wipe_data() {
  [ -d "${DATA}" ] || return 0
  rm -rf "${DATA}" 2>/dev/null && return 0

  log "removing container-owned files"
  docker run --rm -v "${DATA}:/data" alpine:3 sh -c 'rm -rf /data/..?* /data/.[!.]* /data/*' >/dev/null 2>&1 || true
  rm -rf "${DATA}" 2>/dev/null || true
}

# video PATH MINUTES SIZE [LANGUAGE] - black frames at one a second and a
# silent audio track, so a 45-minute 1080p file is under half a megabyte.
# Sonarr 4 will not import a file with no audio track, and FLAC stores silence
# as next to nothing. The track is tagged with the language given, or und
# (undetermined) - Matroska reads a missing tag as English, which would give
# the language audit an answer the fixture never meant to. -nostdin keeps
# ffmpeg from eating the while-read loop's stdin.
video() {
  mkdir -p "$(dirname "$1")"
  local secs=$(($2 * 60))
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "color=c=black:s=$3:r=1" -f lavfi -i "anullsrc=r=8000:cl=mono" -t "$secs" \
    -c:v libx264 -preset ultrafast -crf 51 -g 3600 -pix_fmt yuv420p \
    -c:a flac -compression_level 0 -metadata:s:a:0 "language=${4:-und}" "$1"
}

# config writes Sonarr's config.xml before its first start, so the API key is
# ours, the forms login is off (the API key is what the suites use), and it
# phones nothing home.
config() {
  local key=$1
  mkdir -p "${DATA}/config"
  cat >"${DATA}/config/config.xml" <<EOF
<Config>
  <BindAddress>*</BindAddress>
  <Port>8989</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>False</LaunchBrowser>
  <ApiKey>${key}</ApiKey>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <Branch>main</Branch>
  <LogLevel>debug</LogLevel>
  <UrlBase></UrlBase>
  <InstanceName>Sonarr</InstanceName>
  <AnalyticsEnabled>False</AnalyticsEnabled>
</Config>
EOF
}

fixtures() {
  log "generating the library under ${DATA}"
  wipe_data
  mkdir -p "${DATA}/tv" "${DATA}/downloads/complete" "${DATA}/downloads/incomplete"
  while IFS='|' read -r folder season _ name minutes size lang; do
    [ -n "$folder" ] || continue
    video "${DATA}/tv/${folder}/Season ${season}/${name}.mkv" "$minutes" "$size" "$lang"
  done <<<"$FILES"
  # Sonarr runs as the host's uid through PUID, but a runner that is not uid
  # 1000 and a VM that maps ids differently both need the tree open
  chmod -R 777 "${DATA}"
}

# proxy_ca mints the certificate authority the suites' proxy signs with,
# once, so every container started here trusts the same one.
proxy_ca() {
  [ -f "${PROXY_CA}/ca.pem" ] && [ -f "${PROXY_CA}/ca.key" ] && return 0
  command -v openssl >/dev/null || { echo "openssl is required to mint the provider proxy CA" >&2; exit 1; }
  log "minting the provider proxy CA under ${PROXY_CA}"
  mkdir -p "${PROXY_CA}"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
    -subj "/CN=sonarr-mcp provider proxy CA" \
    -keyout "${PROXY_CA}/ca.key" -out "${PROXY_CA}/ca.pem" 2>/dev/null
  chmod 644 "${PROXY_CA}/ca.pem"
}

# logs prints what Sonarr wrote about itself: the container's stdout, and the
# errors in its own log file, which is where an import or a provider call
# says what went wrong.
logs() {
  echo "==> docker logs ${NAME}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  for f in "${DATA}"/config/logs/sonarr.txt "${DATA}"/config/logs/sonarr.debug.txt; do
    [ -f "$f" ] || continue
    echo "==> ${f} (errors)" >&2
    grep -iE '\|(Error|Warn|Fatal)\||exception' "$f" | tail -60 >&2
    echo "==> ${f} (tail)" >&2
    tail -40 "$f" >&2
  done
}

up() {
  command -v ffmpeg >/dev/null || { echo "ffmpeg is required to generate fixtures" >&2; exit 1; }
  command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }

  down >/dev/null 2>&1 || true
  fixtures
  proxy_ca

  local key
  key=$(openssl rand -hex 16)
  config "$key"
  chmod -R 777 "${DATA}/config"

  local host no_proxy
  host="$(proxy_host)"
  no_proxy="localhost,127.0.0.1,${NAME},host.docker.internal,${host}"
  log "creating ${IMAGE} as ${NAME} on ${PORT} (providers proxied via ${host}:${PROXY_PORT})"
  docker create --name "$NAME" \
    -p "${PORT}:8989" \
    --hostname "$NAME" \
    --add-host "host.docker.internal:host-gateway" \
    -e "PUID=$(id -u)" -e "PGID=$(id -g)" -e "TZ=Etc/UTC" \
    -e "HTTP_PROXY=http://${host}:${PROXY_PORT}" \
    -e "HTTPS_PROXY=http://${host}:${PROXY_PORT}" \
    -e "http_proxy=http://${host}:${PROXY_PORT}" \
    -e "https_proxy=http://${host}:${PROXY_PORT}" \
    -e "NO_PROXY=${no_proxy}" -e "no_proxy=${no_proxy}" \
    -e "SSL_CERT_FILE=/proxy/ca.pem" \
    -v "${DATA}/config:/config" -v "${DATA}/tv:/tv" -v "${DATA}/downloads:/downloads" \
    -v "${PROXY_CA}/ca.pem:/proxy/ca.pem:ro" \
    "$IMAGE" >/dev/null

  # consumed with eval "$(scripts/testenv.sh up)"
  echo "export SONARR_SERVER='${URL}'"
  echo "export SONARR_TOKEN='${key}'"
  echo "export SONARR_TEST_CONTAINER='${NAME}'"
  echo "export SONARR_TEST_DATA='${DATA}'"
  echo "export SONARR_TEST_HOST='${host}'"
  echo "export SONARR_TEST_PROXY_PORT='${PROXY_PORT}'"
  echo "export SONARR_TEST_INDEXER_PORT='${INDEXER_PORT}'"
  echo "export SONARR_TEST_SAB_PORT='${SAB_PORT}'"
  echo "export SONARR_TEST_PROXY_CA='${PROXY_CA}'"
}

down() {
  log "removing ${NAME}"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  wipe_data
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  fixtures) fixtures ;;
  logs) logs ;;
  *) echo "usage: $0 [up|down|fixtures|logs]" >&2; exit 1 ;;
esac
