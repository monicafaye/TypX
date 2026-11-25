#!/bin/sh
set -eu

REPO="monicafaye/TypX"
INSTALL_DIR="${TYPX_INSTALL_DIR:-${HOME}/.local/bin}"
VERSION="${TYPX_VERSION:-}"
SKIP_TINYMIST="${TYPX_SKIP_TINYMIST:-0}"
NO_MODIFY_PATH="${TYPX_NO_MODIFY_PATH:-0}"

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
    printf 'error: unsupported OS %s (for Windows, run: irm https://raw.githubusercontent.com/%s/main/scripts/install.ps1 | iex)\n' "${OS_RAW}" "${REPO}" >&2
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

PATH_UPDATED=0

find_tinymist() {
  if command -v tinymist >/dev/null 2>&1; then
    command -v tinymist
    return 0
  fi
  for cand in \
    "/opt/homebrew/bin/tinymist" \
    "/usr/local/bin/tinymist" \
    "${HOME}/.local/bin/tinymist" \
    "${HOME}/.cargo/bin/tinymist"; do
    if [ -x "${cand}" ]; then
      printf '%s\n' "${cand}"
      return 0
    fi
  done
  return 1
}

if [ "${SKIP_TINYMIST}" != "1" ]; then
  if EXISTING_TINYMIST="$(find_tinymist)"; then
    printf 'Found existing tinymist at %s.\n' "${EXISTING_TINYMIST}"
  else
    printf '\nInstalling tinymist (required for live preview, diagnostics, and PDF export)...\n'
    if curl --proto '=https' --tlsv1.2 -LsSf "https://github.com/Myriad-Dreamin/tinymist/releases/latest/download/tinymist-installer.sh" | sh; then
      PATH_UPDATED=1
    else
      printf 'warning: could not install tinymist automatically.\n' >&2
      printf 'TypX is installed. Install tinymist manually from:\n' >&2
      printf '  https://github.com/Myriad-Dreamin/tinymist/releases\n' >&2
    fi
  fi
fi

add_to_profile() {
  rc_file="$1"
  export_line="export PATH=\"${INSTALL_DIR}:\$PATH\""
  if [ -f "${rc_file}" ]; then
    if grep -F "${INSTALL_DIR}" "${rc_file}" >/dev/null 2>&1; then
      return 0
    fi
    if [ "${INSTALL_DIR}" = "${HOME}/.local/bin" ] && grep -F '$HOME/.local/bin' "${rc_file}" >/dev/null 2>&1; then
      return 0
    fi
  fi
  printf '\n# Added by TypX installer\n%s\n' "${export_line}" >> "${rc_file}"
  printf 'Added %s to %s\n' "${INSTALL_DIR}" "${rc_file}"
  PATH_UPDATED=1
}

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    if [ "${NO_MODIFY_PATH}" = "1" ]; then
      printf '\nNote: %s is not on your PATH. Add it to your shell profile:\n' "${INSTALL_DIR}"
      printf '  export PATH="%s:$PATH"\n' "${INSTALL_DIR}"
    else
      SHELL_NAME="$(basename "${SHELL:-}")"
      case "${SHELL_NAME}" in
        zsh)  add_to_profile "${HOME}/.zshrc" ;;
        bash)
          if [ "${OS}" = "darwin" ]; then
            add_to_profile "${HOME}/.bash_profile"
          else
            add_to_profile "${HOME}/.bashrc"
          fi
          ;;
        *)
          if [ "${OS}" = "darwin" ]; then
            add_to_profile "${HOME}/.zshrc"
          else
            add_to_profile "${HOME}/.profile"
          fi
          ;;
      esac
    fi
    ;;
esac

if [ "${PATH_UPDATED}" = "1" ]; then
  printf '\nClose this terminal window and open a new one, then run:\n'
else
  printf '\nVerify your setup by running:\n'
fi
printf '  typx --check\n'
