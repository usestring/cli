# The release workflow writes this file with tools/release for each release
# and commits it to main, so a change to it belongs in tools/release.
class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  version "0.3.1"
  license "MIT"

  livecheck do
    url :stable
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  on_macos do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.1/string_darwin_arm64.tar.gz"
      sha256 "71fe0fa21fdeb87031e2e9565215dcba94b63a9d8de2ea4e56de1277b2b58b70"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.1/string_darwin_amd64.tar.gz"
      sha256 "b384898c5bd8353c60f779acbb022314cf792646879b148c18707a59b3e726aa"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.1/string_linux_arm64.tar.gz"
      sha256 "fe328f569260e27869badf21c1fb892fdf7ad06ba1ea0495b74229c44084ca95"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.1/string_linux_amd64.tar.gz"
      sha256 "1a57c587eeefe82abc5e4008948a8dbc644ae700cfa7c374497e1bca0c7db594"
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
