# The release workflow writes this file with tools/release for each release
# and commits it to main, so a change to it belongs in tools/release.
class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  version "0.2.1"
  license "MIT"

  livecheck do
    url :stable
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  on_macos do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.2.1/string_darwin_arm64.tar.gz"
      sha256 "18351d4517481669f5adb595005eefd8778907afd3dd14bf1ad86dc1cfab92ce"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.2.1/string_darwin_amd64.tar.gz"
      sha256 "fbcdbb72f3f5197c5d99695ba13a466fc31a1fd0d38c1e2c53285c817a9d36aa"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.2.1/string_linux_arm64.tar.gz"
      sha256 "fa81f4594714b125ac1d681a3fe0b051bda0ea9b762776fec663ba7dc404751e"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.2.1/string_linux_amd64.tar.gz"
      sha256 "f59bd60e3ae012110a91dafbb6577d19041fe935ed8131ffdf830657f5e52db0"
    end
  end

  def install
    bin.install "string"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/string --version")

    # Without a key, fetch and search go out over the network; a key-only command is refused
    # before any request, so it checks the missing-key path offline.
    ENV.delete("STRING_API_KEY")
    ENV.delete("XDG_CONFIG_HOME")
    assert_match "needs a key", shell_output("#{bin}/string sitemap list 2>&1", 3)
  end
end
