#!/usr/bin/env bash
set -euo pipefail

select_major_release() {
  local major=$1
  node -e '
    const fs = require("fs");
    const major = process.argv[1];
    const releases = JSON.parse(fs.readFileSync(0, "utf8"));
    const pattern = new RegExp("^v" + major + "\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$");
    const matches = releases
      .filter((release) => release && release.prerelease === false && release.draft === false)
      .map((release) => ({ tag: release.tag_name, match: pattern.exec(release.tag_name) }))
      .filter((release) => release.match)
      .map((release) => ({
        tag: release.tag,
        minor: BigInt(release.match[1]),
        patch: BigInt(release.match[2]),
      }))
      .sort((a, b) => a.minor < b.minor ? -1 : a.minor > b.minor ? 1
        : a.patch < b.patch ? -1 : a.patch > b.patch ? 1 : 0);
    if (matches.length > 0) process.stdout.write(matches[matches.length - 1].tag);
  ' "${major}"
}

verify_archive_checksum() {
  local archive_path=$1
  local checksums_path=$2
  local archive_name=$3
  local expected actual

  expected=$(awk -v file="${archive_name}" '
    ($2 == file || $2 == "*" file) { count++; hash = $1 }
    END { if (count == 1) print hash; else exit 1 }
  ' "${checksums_path}") || {
    echo "checksums.txt has no unique SHA-256 entry for ${archive_name}" >&2
    return 1
  }
  if [[ ! "${expected}" =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "checksums.txt has no unique SHA-256 entry for ${archive_name}" >&2
    return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum < "${archive_path}" | awk '{print $1}')
  else
    actual=$(shasum -a 256 < "${archive_path}" | awk '{print $1}')
  fi
  if [[ "${actual}" != "${expected}" ]]; then
    echo "SHA-256 verification failed for ${archive_name}" >&2
    return 1
  fi
}

main() {
if [[ -n "${TRASLOCO_GITHUB_TOKEN:-}" ]]; then
  echo "::add-mask::${TRASLOCO_GITHUB_TOKEN}"
fi

case "${TRASLOCO_INPUT_FORMAT:-}" in
  text|sarif) ;;
  *) echo "format must be text or sarif" >&2; exit 64 ;;
esac
case "${TRASLOCO_FAIL_ON_FINDINGS:-}" in
  true|false) ;;
  *) echo "fail-on-findings must be true or false" >&2; exit 64 ;;
esac
if [[ ! "${TRASLOCO_ACTION_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "invalid action repository" >&2
  exit 1
fi
if [[ -z "${TRASLOCO_INPUT_PATH:-}" ]]; then
  echo "path must not be empty" >&2
  exit 64
fi
if [[ "${TRASLOCO_INPUT_PATH}" == -* ]]; then
  echo "path must not start with '-'" >&2
  exit 64
fi

requested_version=${TRASLOCO_VERSION:-${TRASLOCO_ACTION_REF:-}}
if [[ "${requested_version}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  version=${requested_version}
elif [[ "${requested_version}" =~ ^v(0|[1-9][0-9]*)$ ]]; then
  major=${BASH_REMATCH[1]}
  curl_headers=()
  if [[ -n "${TRASLOCO_GITHUB_TOKEN:-}" ]]; then
    curl_headers+=(--header "Authorization: Bearer ${TRASLOCO_GITHUB_TOKEN}")
  fi
  releases=$(curl --fail --location --silent --show-error --retry 3 \
    "${curl_headers[@]}" \
    "https://api.github.com/repos/${TRASLOCO_ACTION_REPOSITORY}/releases?per_page=100")
  version=$(printf '%s' "${releases}" | select_major_release "${major}")
  if [[ -z "${version}" ]]; then
    echo "no non-prerelease v${major}.x.y release found" >&2
    exit 1
  fi
else
  echo "version must be an exact vMAJOR.MINOR.PATCH tag or a vMAJOR alias (set the version input when the action is pinned by commit SHA)" >&2
  exit 64
fi

case "$(uname -s)" in
  Linux*) os=linux ;;
  Darwin*) os=darwin ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) echo "unsupported runner OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported runner architecture: $(uname -m)" >&2; exit 1 ;;
esac

release_version=${version#v}
archive="trasloco_${release_version}_${os}_${arch}.tar.gz"
if [[ "${os}" == windows ]]; then archive="trasloco_${release_version}_${os}_${arch}.zip"; fi
base_url="https://github.com/${TRASLOCO_ACTION_REPOSITORY}/releases/download/${version}"
work_dir=$(mktemp -d "${RUNNER_TEMP%/}/trasloco.XXXXXX")
cleanup() { rm -rf -- "${work_dir}"; }
trap cleanup EXIT
curl --fail --location --silent --show-error --retry 3 --output "${work_dir}/${archive}" "${base_url}/${archive}"
curl --fail --location --silent --show-error --retry 3 --output "${work_dir}/checksums.txt" "${base_url}/checksums.txt"

verify_archive_checksum "${work_dir}/${archive}" "${work_dir}/checksums.txt" "${archive}"

if [[ "${os}" == windows ]]; then
  unzip -q "${work_dir}/${archive}" -d "${work_dir}/bin"
  executable="${work_dir}/bin/trasloco.exe"
else
  mkdir -p "${work_dir}/bin"
  tar -xzf "${work_dir}/${archive}" -C "${work_dir}/bin"
  executable="${work_dir}/bin/trasloco"
fi

args=(modernize --format "${TRASLOCO_INPUT_FORMAT}")
if [[ "${TRASLOCO_FAIL_ON_FINDINGS}" == true ]]; then args+=(--check); fi
args+=(-- "${TRASLOCO_INPUT_PATH}")
if [[ "${TRASLOCO_INPUT_FORMAT}" == sarif ]]; then
  status=0
  "${executable}" "${args[@]}" > "${TRASLOCO_SARIF_FILE}" || status=$?
  # Only a complete report (exit 0 or 5) is uploaded to code scanning.
  if [[ ( "${status}" -eq 0 || "${status}" -eq 5 ) \
    && -f "${TRASLOCO_SARIF_FILE}" && -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "sarif-written=true" >> "${GITHUB_OUTPUT}"
  fi
  exit "${status}"
else
  "${executable}" "${args[@]}"
fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
