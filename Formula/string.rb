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
    system "go", "build", *std_go_args, "./cmd/string"
  end

  test do
    ENV.delete("STRING_API_KEY")
    assert_match "Set STRING_API_KEY to a String Web Access API key",
                 shell_output("#{bin}/string fetch https://example.com 2>&1", 1)
  end
end
