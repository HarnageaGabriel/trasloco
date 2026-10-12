#!/usr/bin/env bash
set -euo pipefail

case "${TRASLOCO_INPUT_FORMAT}" in
  text|sarif) ;;
  *) echo "format must be text or sarif" >&2; exit 64 ;;
esac
case "${TRASLOCO_FAIL_ON_FINDINGS}" in
  true|false) ;;
  *) echo "fail-on-findings must be true or false" >&2; exit 64 ;;
esac
if [[ ! "${TRASLOCO_ACTION_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "invalid action repository" >&2
  exit 1
fi
if [[ -z "${TRASLOCO_INPUT_PATH}" ]]; then
  echo "path must not be empty" >&2
  exit 64
fi
if [[ "${TRASLOCO_INPUT_PATH}" == -* ]]; then
  echo "path must not start with '-'" >&2
  exit 64
fi

version=${TRASLOCO_VERSION}
if [[ -z "${version}" ]]; then
  if [[ "${TRASLOCO_ACTION_REF:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
    version=${TRASLOCO_ACTION_REF}
  elif [[ "${TRASLOCO_ACTION_REF:-}" =~ ^v([0-9]+)$ ]]; then
    major=${BASH_REMATCH[1]}
    if ! command -v jq >/dev/null 2>&1; then
      echo "jq is required to resolve the ${TRASLOCO_ACTION_REF} release" >&2
      exit 1
    fi
    releases=$(curl --fail --location --silent --show-error --retry 3 \
      "https://api.github.com/repos/${TRASLOCO_ACTION_REPOSITORY}/releases?per_page=100")
    version=$(printf '%s' "${releases}" | jq -r --arg major "v${major}" '
      [.[] | select(.prerelease == false and .draft == false) | .tag_name
        | select(test("^" + $major + "\\.[0-9]+\\.[0-9]+$"))]
      | sort_by(ltrimstr("v") | split(".") | map(tonumber)) | last // empty')
    if [[ -z "${version}" ]]; then
      echo "no non-prerelease ${TRASLOCO_ACTION_REF}.x.y release found" >&2
      exit 1
    fi
  else
    latest_release_url=$(curl --fail --location --silent --show-error --retry 3 \
      --output /dev/null --write-out '%{url_effective}' \
      "https://github.com/${TRASLOCO_ACTION_REPOSITORY}/releases/latest")
    version=${latest_release_url##*/}
  fi
fi
if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "version must resolve to a release tag such as v0.1.0" >&2
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

expected=$(awk -v file="${archive}" '$2 == file || $2 == "*" file { print $1 }' "${work_dir}/checksums.txt")
if [[ ! "${expected}" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "checksums.txt has no unique SHA-256 entry for ${archive}" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum < "${work_dir}/${archive}" | awk '{print $1}')
else
  actual=$(shasum -a 256 < "${work_dir}/${archive}" | awk '{print $1}')
fi
if [[ "${actual}" != "${expected}" ]]; then
  echo "SHA-256 verification failed for ${archive}" >&2
  exit 1
fi

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
  if [[ ( "${status}" -eq 0 || "${status}" -eq 5 ) && -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "sarif-written=true" >> "${GITHUB_OUTPUT}"
  fi
  exit "${status}"
else
  "${executable}" "${args[@]}"
fi
