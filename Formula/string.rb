# The release workflow writes this file with tools/release for each release
# and commits it to main, so a change to it belongs in tools/release.
class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  version "0.3.0"
  license "MIT"

  livecheck do
    url :stable
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  on_macos do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_darwin_arm64.tar.gz"
      sha256 "5db93a083c9d1fff9eac1fee8f4395ce431a5a6be3ca6481f0f01458441797b9"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_darwin_amd64.tar.gz"
      sha256 "ebf9361ce051fcfbc57898e11733779c9fd82590373eec6303a0b2753f353895"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_linux_arm64.tar.gz"
      sha256 "85eef580c2e8eedb69a058dcfca33ccf663c72a9bf2134ac7d8f439ec09b1530"
    end
    on_intel do
      url "https://github.com/usestring/cli/releases/download/v0.3.0/string_linux_amd64.tar.gz"
      sha256 "8af5cce6bdc569718c3947d5361fd3cf578db8758c7ac1853fe46b6062bad6f9"
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
