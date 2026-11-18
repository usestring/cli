# The release workflow writes this file with tools/release for each release
# and commits it to main, so a change to it belongs in tools/release.
class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  license "MIT"

  livecheck do
    url :stable
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  on_macos do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_darwin_arm64.tar.gz"
      sha256 "519c5d8ffa031c4aab4f3c57791161b60358a1b53c266e9cdac6ffe1710e653c"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_darwin_amd64.tar.gz"
      sha256 "0c1d43412b76e597b41763b066bda72cd457e26c0169a542a7abaebf2f5ed250"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_linux_arm64.tar.gz"
      sha256 "bb20183cb2823473103f48c623fd5875260d491f74f5a9f09822075812b9e007"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_linux_amd64.tar.gz"
      sha256 "15833ebb4e50e3f5b98686b75e6f487836150eae2805f292c5c849a5f2beadcf"
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
