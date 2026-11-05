class String < Formula
  desc "Command-line access to String Web Access"
  homepage "https://github.com/usestring/cli"
  url "https://github.com/usestring/cli/releases/download/v0.1.0/string-0.1.0.tar.gz"
  sha256 "e01321aa3912f30db663a50d05c86fa492f6869e1818f733964417d80197b8a3"
  license "MIT"

  livecheck do
    url "https://github.com/usestring/cli.git"
    regex(/^v?(\d+(?:\.\d+)+)$/i)
  end

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"
    ldflags = "-s -w -X main.version=#{version}"
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
