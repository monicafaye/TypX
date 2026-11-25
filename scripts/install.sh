#!/bin/sh
set -eu

REPO="monicafaye/TypX"
INSTALL_DIR="${TYPX_INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${TYPX_VERSION:-}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'error: required command not found: %s\n' "$1" >&2
    exit 1
  fi
}

need_cmd curl
need_cmd tar
need_cmd uname
need_cmd mktemp

OS_RAW="$(uname -s)"
case "${OS_RAW}" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)
    printf 'error: unsupported OS %s (for Windows, use Scoop or download from https://github.com/%s/releases)\n' "${OS_RAW}" "${REPO}" >&2
    exit 1
    ;;
esac

ARCH_RAW="$(uname -m)"
case "${ARCH_RAW}" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    printf 'error: unsupported architecture %s (supported: amd64, arm64)\n' "${ARCH_RAW}" >&2
    exit 1
    ;;
esac

if [ -z "${VERSION}" ]; then
  EFFECTIVE_URL="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
  TAG="${EFFECTIVE_URL##*/}"
  if [ -z "${TAG}" ] || [ "${TAG}" = "latest" ] || [ "${TAG}" = "releases" ]; then
    printf 'error: could not determine latest TypX release tag\n' >&2
    exit 1
  fi
  VERSION="${TAG#v}"
else
  VERSION="${VERSION#v}"
  TAG="v${VERSION}"
fi

ARCHIVE="typx-${VERSION}-${OS}-${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT INT TERM

printf 'Downloading TypX %s (%s/%s)...\n' "${VERSION}" "${OS}" "${ARCH}"
curl -fsSL "${BASE_URL}/${ARCHIVE}" -o "${TMP_DIR}/${ARCHIVE}"
curl -fsSL "${BASE_URL}/SHA256SUMS.txt" -o "${TMP_DIR}/SHA256SUMS.txt"

EXPECTED_SHA=""
while read -r sum name; do
  name="${name#\*}"
  if [ "${name}" = "${ARCHIVE}" ]; then
    EXPECTED_SHA="${sum}"
    break
  fi
done < "${TMP_DIR}/SHA256SUMS.txt"

if [ -z "${EXPECTED_SHA}" ]; then
  printf 'error: checksum for %s not found in SHA256SUMS.txt\n' "${ARCHIVE}" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA="$(sha256sum "${TMP_DIR}/${ARCHIVE}" | cut -d ' ' -f 1)"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA="$(shasum -a 256 "${TMP_DIR}/${ARCHIVE}" | cut -d ' ' -f 1)"
else
  printf 'error: neither sha256sum nor shasum found to verify download\n' >&2
  exit 1
fi

if [ "${ACTUAL_SHA}" != "${EXPECTED_SHA}" ]; then
  printf 'error: SHA-256 mismatch for %s\n  expected: %s\n  actual:   %s\n' "${ARCHIVE}" "${EXPECTED_SHA}" "${ACTUAL_SHA}" >&2
  exit 1
fi
printf 'Verified SHA-256 checksum.\n'

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "${TMP_DIR}"
BIN_SRC="${TMP_DIR}/typx-${VERSION}-${OS}-${ARCH}/typx"
if [ ! -f "${BIN_SRC}" ]; then
  printf 'error: expected binary not found in archive: %s\n' "${BIN_SRC}" >&2
  exit 1
fi

mkdir -p "${INSTALL_DIR}"
cp "${BIN_SRC}" "${INSTALL_DIR}/typx"
chmod 0755 "${INSTALL_DIR}/typx"

printf 'Installed TypX to %s/typx\n' "${INSTALL_DIR}"
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    printf '\nNote: %s is not on your PATH. Add it to your shell profile:\n' "${INSTALL_DIR}"
    printf '  export PATH="%s:$PATH"\n' "${INSTALL_DIR}"
    ;;
esac

printf '\nRun `typx --check` to verify your terminal and tinymist setup.\n'
