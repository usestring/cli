class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  url "https://github.com/usestring/cli/releases/download/v0.2.1/string-0.2.1.tar.gz"
  sha256 "d9268b3b9654a3b1274cdb03155bb9d742b8e7f7a69278793c837e3c23f3166b"
  license "MIT"

  livecheck do
    url "https://github.com/usestring/cli.git"
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"
    ldflags = "-X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags:), "./cmd/string"
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
