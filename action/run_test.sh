#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/trasloco-action-test.XXXXXX")
cleanup() { rm -rf -- "${test_root}"; }
trap cleanup EXIT
stub_dir=${test_root}/bin
state_dir=${test_root}/state
mkdir -p "${stub_dir}" "${state_dir}"

cat > "${stub_dir}/uname" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -m ]]; then printf '%s\n' x86_64; else printf '%s\n' Linux; fi
EOF

cat > "${stub_dir}/curl" <<'EOF'
#!/usr/bin/env bash
state=${TEST_STATE_DIR}
count_file="${state}/curl-count"
count=0
[[ -f "${count_file}" ]] && count=$(<"${count_file}")
printf '%s' "$((count + 1))" > "${count_file}"
printf '%s\n' "$@" > "${state}/curl-argv-${count}"
: > "${state}/curl-stdin-${count}"
output=
url=
config_stdin=0
while (($#)); do
  case "$1" in
    --output) output=$2 ;;
    -K) [[ "${2:-}" == "-" ]] && config_stdin=1 ;;
  esac
  url=$1
  shift
done
if [[ "${config_stdin}" == 1 ]]; then
  cat > "${state}/curl-stdin-${count}"
fi
case "${url}" in
  https://api.github.com/*)
    printf '%s\n' '[{"tag_name":"v0.2.0","prerelease":false,"draft":false}]'
    ;;
  */checksums.txt)
    printf '%s  %s\n' e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 trasloco_0.2.0_linux_amd64.tar.gz > "${output}"
    ;;
  */checksums.txt.sigstore.json) printf '%s\n' '{}' > "${output}" ;;
  *) : > "${output}" ;;
esac
EOF

cat > "${stub_dir}/cosign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" > "${TEST_STATE_DIR}/cosign-args"
if [[ "${COSIGN_FAIL:-0}" == 1 ]]; then exit 9; fi
EOF

cat > "${stub_dir}/tar" <<'EOF'
#!/usr/bin/env bash
destination=
while (($#)); do
  if [[ "$1" == -C ]]; then destination=$2; shift 2; else shift; fi
done
mkdir -p "${destination}"
cat > "${destination}/trasloco" <<'CHILD'
#!/usr/bin/env bash
if [[ -n "${TRASLOCO_GITHUB_TOKEN+x}" ]]; then
  printf '%s\n' present > "${TEST_STATE_DIR}/token"
else
  printf '%s\n' absent > "${TEST_STATE_DIR}/token"
fi
printf '%s\n' "$@" > "${TEST_STATE_DIR}/args"
CHILD
chmod +x "${destination}/trasloco"
EOF

chmod +x "${stub_dir}"/*

run_action() {
  env \
    PATH="${stub_dir}:${PATH}" \
    RUNNER_TEMP="${test_root}" \
    TEST_STATE_DIR="${state_dir}" \
    TRASLOCO_ACTION_REPOSITORY=HarnageaGabriel/trasloco \
    TRASLOCO_GITHUB_TOKEN=super-secret-token \
    TRASLOCO_INPUT_PATH=. \
    TRASLOCO_INPUT_FORMAT=text \
    TRASLOCO_FAIL_ON_FINDINGS=true \
    TRASLOCO_STRICT="${1}" \
    TRASLOCO_VERSION="${3:-v0.2.0}" \
    COSIGN_FAIL="${2:-0}" \
    bash "${repo_root}/action/run.sh"
}

output=$(run_action true 2>&1)
[[ "${output}" != *super-secret-token* ]]
[[ "$(<"${state_dir}/token")" == absent ]]
grep -Fx -- --strict "${state_dir}/args" >/dev/null
grep -F -- 'release.yml@refs/tags/v0.2.0' "${state_dir}/cosign-args" >/dev/null
grep -F -- '--bundle' "${state_dir}/cosign-args" >/dev/null
grep -A3 '^  strict:' "${repo_root}/action.yml" | grep -F "default: 'true'" >/dev/null

# Asset downloads run without the token: no curl argv carries it or an
# Authorization header.
if grep -qF -- 'super-secret-token' "${state_dir}"/curl-argv-*; then
  echo "token leaked into curl argv" >&2
  exit 1
fi
if grep -qF -- 'Authorization' "${state_dir}"/curl-argv-*; then
  echo "Authorization header leaked into curl argv" >&2
  exit 1
fi

# A vMAJOR alias forces a releases API call: the token arrives through curl's -K
# stdin config, never argv, and the following asset downloads stay plain.
rm -f "${state_dir}"/curl-*
run_action true 0 v0 >/dev/null
grep -qF -- '-K' "${state_dir}/curl-argv-0"
grep -qF -- 'header = "Authorization: Bearer super-secret-token"' "${state_dir}/curl-stdin-0"
if grep -qF -- 'super-secret-token' "${state_dir}"/curl-argv-*; then
  echo "token leaked into curl argv" >&2
  exit 1
fi
if grep -qF -- 'Authorization' "${state_dir}/curl-argv-0" \
  "${state_dir}/curl-argv-1" "${state_dir}/curl-argv-2" "${state_dir}/curl-argv-3"; then
  echo "Authorization header leaked into curl argv" >&2
  exit 1
fi
if grep -qF -- 'Authorization' "${state_dir}/curl-stdin-1" \
  "${state_dir}/curl-stdin-2" "${state_dir}/curl-stdin-3"; then
  echo "asset download carried an Authorization header" >&2
  exit 1
fi

rm -f "${state_dir}/args" "${state_dir}/token"
if output=$(run_action true 1 2>&1); then
  echo "cosign failure unexpectedly succeeded" >&2
  exit 1
fi
[[ ! -e "${state_dir}/args" ]]

if output=$(run_action invalid 2>&1); then
  echo "invalid strict input unexpectedly succeeded" >&2
  exit 1
fi
[[ "${output}" == *"strict must be true or false"* ]]

echo "action/run_test.sh: PASS"
