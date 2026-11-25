#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "Usage: $0 <version> <sha256sums-file> [output-dir]" >&2
  exit 1
fi

VERSION="${1#v}"
SUMS_FILE="$2"
OUT_DIR="${3:-packaging}"

if [ ! -f "${SUMS_FILE}" ]; then
  echo "SHA256SUMS file not found: ${SUMS_FILE}" >&2
  exit 1
fi

lookup_sha() {
  local target="$1"
  local sum name
  while read -r sum name; do
    name="${name#\*}"
    if [ "${name}" = "${target}" ]; then
      printf '%s' "${sum}"
      return 0
    fi
  done < "${SUMS_FILE}"
  echo "Missing checksum for ${target} in ${SUMS_FILE}" >&2
  return 1
}

SHA_WIN_AMD64="$(lookup_sha "typx-${VERSION}-windows-amd64.zip")"
SHA_WIN_ARM64="$(lookup_sha "typx-${VERSION}-windows-arm64.zip")"
SHA_DARWIN_ARM64="$(lookup_sha "typx-${VERSION}-darwin-arm64.tar.gz")"
SHA_DARWIN_AMD64="$(lookup_sha "typx-${VERSION}-darwin-amd64.tar.gz")"
SHA_LINUX_ARM64="$(lookup_sha "typx-${VERSION}-linux-arm64.tar.gz")"
SHA_LINUX_AMD64="$(lookup_sha "typx-${VERSION}-linux-amd64.tar.gz")"

mkdir -p "${OUT_DIR}/scoop" "${OUT_DIR}/homebrew" "${OUT_DIR}/aur"

cat > "${OUT_DIR}/scoop/typx.json" <<EOF
{
    "version": "${VERSION}",
    "description": "Terminal editor for Typst with live math concealment, document outline, tinymist LSP, and Sixel preview",
    "homepage": "https://github.com/monicafaye/TypX",
    "license": "MIT",
    "architecture": {
        "64bit": {
            "url": "https://github.com/monicafaye/TypX/releases/download/v${VERSION}/typx-${VERSION}-windows-amd64.zip",
            "hash": "${SHA_WIN_AMD64}"
        },
        "arm64": {
            "url": "https://github.com/monicafaye/TypX/releases/download/v${VERSION}/typx-${VERSION}-windows-arm64.zip",
            "hash": "${SHA_WIN_ARM64}"
        }
    },
    "bin": "typx.exe",
    "suggest": {
        "LSP diagnostics, completion, hover, and live preview": "tinymist"
    },
    "checkver": {
        "github": "https://github.com/monicafaye/TypX"
    },
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/monicafaye/TypX/releases/download/v\$version/typx-\$version-windows-amd64.zip"
            },
            "arm64": {
                "url": "https://github.com/monicafaye/TypX/releases/download/v\$version/typx-\$version-windows-arm64.zip"
            }
        },
        "hash": {
            "url": "\$baseurl/SHA256SUMS.txt"
        }
    }
}
EOF

cat > "${OUT_DIR}/homebrew/typx.rb" <<EOF
class Typx < Formula
  desc "Terminal editor for Typst with live math concealment, outline, tinymist LSP, and Sixel preview"
  homepage "https://github.com/monicafaye/TypX"
  version "${VERSION}"
  license "MIT"

  depends_on "tinymist"

  on_macos do
    on_arm do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-darwin-arm64.tar.gz"
      sha256 "${SHA_DARWIN_ARM64}"
    end

    on_intel do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-darwin-amd64.tar.gz"
      sha256 "${SHA_DARWIN_AMD64}"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-linux-arm64.tar.gz"
      sha256 "${SHA_LINUX_ARM64}"
    end

    on_intel do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-linux-amd64.tar.gz"
      sha256 "${SHA_LINUX_AMD64}"
    end
  end

  def install
    bin.install "typx"
    doc.install "README.md", "LICENSE", "LICENSE-MICRO", "LICENSE-THIRD-PARTY"
  end

  def caveats
    <<~EOS
      Run the check command to verify your terminal and tinymist setup:
        typx --check
    EOS
  end

  test do
    assert_match "TypX #{version}", shell_output("#{bin}/typx --version")
  end
end
EOF

cat > "${OUT_DIR}/aur/PKGBUILD" <<EOF
# Maintainer: monicafaye <354112421+monicafaye@users.noreply.github.com>
pkgname=typx-bin
pkgver=${VERSION}
pkgrel=1
pkgdesc='Terminal editor for Typst with live math concealment, document outline, tinymist LSP, and Sixel preview'
arch=('x86_64' 'aarch64')
url='https://github.com/monicafaye/TypX'
license=('MIT')
provides=('typx')
conflicts=('typx')
optdepends=(
  'tinymist: Typst language server for diagnostics, completion, hover, formatting, and live preview'
  'wl-clipboard: Wayland system clipboard integration'
  'xclip: X11 system clipboard integration'
)
source_x86_64=("\${url}/releases/download/v\${pkgver}/typx-\${pkgver}-linux-amd64.tar.gz")
source_aarch64=("\${url}/releases/download/v\${pkgver}/typx-\${pkgver}-linux-arm64.tar.gz")
sha256sums_x86_64=('${SHA_LINUX_AMD64}')
sha256sums_aarch64=('${SHA_LINUX_ARM64}')

package() {
  local goarch='amd64'
  if [ "\${CARCH}" = 'aarch64' ]; then
    goarch='arm64'
  fi

  local srcdir_pkg="\${srcdir}/typx-\${pkgver}-linux-\${goarch}"
  install -Dm755 "\${srcdir_pkg}/typx" "\${pkgdir}/usr/bin/typx"
  install -Dm644 "\${srcdir_pkg}/LICENSE" "\${pkgdir}/usr/share/licenses/\${pkgname}/LICENSE"
  install -Dm644 "\${srcdir_pkg}/LICENSE-MICRO" "\${pkgdir}/usr/share/licenses/\${pkgname}/LICENSE-MICRO"
  install -Dm644 "\${srcdir_pkg}/LICENSE-THIRD-PARTY" "\${pkgdir}/usr/share/licenses/\${pkgname}/LICENSE-THIRD-PARTY"
  install -Dm644 "\${srcdir_pkg}/README.md" "\${pkgdir}/usr/share/doc/\${pkgname}/README.md"
}
EOF
