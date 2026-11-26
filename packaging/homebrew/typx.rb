class Typx < Formula
  desc "Terminal editor for Typst with live math concealment, outline, tinymist LSP, and Kitty/Sixel preview"
  homepage "https://github.com/monicafaye/TypX"
  version "0.3.0"
  license "MIT"

  depends_on "tinymist"

  on_macos do
    on_arm do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-darwin-arm64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    end

    on_intel do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-darwin-amd64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-linux-arm64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_ARM64_SHA256"
    end

    on_intel do
      url "https://github.com/monicafaye/TypX/releases/download/v#{version}/typx-#{version}-linux-amd64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
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
