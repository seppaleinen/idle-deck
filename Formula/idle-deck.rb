class IdleDeck < Formula
  desc "GitHub issue automation for LLM-assisted development"
  homepage "https://github.com/seppaleinen/idle-deck"
  license "MIT"

  # ---- Version / releases ----
  # bump VERSION when cutting a release; the tag and release must match
  VERSION = "0.1.0".freeze

  # stable: download the binary from the GitHub Release assets
  url "https://github.com/seppaleinen/idle-deck/releases/download/v#{VERSION}/idle-deck_#{VERSION}_darwin_arm64.tar.gz"
  sha256 "" # filled by release workflow
  version VERSION

  # head: install directly from main via `go install`
  head do
    url "https://github.com/seppaleinen/idle-deck.git", branch: "main"
    depends_on "go" => :build
  end

  # ---- Platform-specific binary downloads ----
  # The release workflow produces platform-specific tarballs:
  # - idle-deck_{version}_darwin_arm64.tar.gz
  # - idle-deck_{version}_darwin_amd64.tar.gz
  # - idle-deck_{version}_linux_arm64.tar.gz
  # - idle-deck_{version}_linux_amd64.tar.gz

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/seppaleinen/idle-deck/releases/download/v#{VERSION}/idle-deck_#{VERSION}_darwin_arm64.tar.gz"
      sha256 "" # filled by release workflow
    else
      url "https://github.com/seppaleinen/idle-deck/releases/download/v#{VERSION}/idle-deck_#{VERSION}_darwin_amd64.tar.gz"
      sha256 "" # filled by release workflow
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/seppaleinen/idle-deck/releases/download/v#{VERSION}/idle-deck_#{VERSION}_linux_arm64.tar.gz"
      sha256 "" # filled by release workflow
    else
      url "https://github.com/seppaleinen/idle-deck/releases/download/v#{VERSION}/idle-deck_#{VERSION}_linux_amd64.tar.gz"
      sha256 "" # filled by release workflow
    end
  end

  # ---- Conflicts ----
  # None for now; idle-deck is a unique binary name

  # ---- Install ----
  def install
    bin.install "idle-deck"
  end

  # ---- Test ----
  def test
    # Basic version check
    output = shell_output("#{bin}/idle-deck --version")
    assert_match "idle-deck #{VERSION}", output

    # Refusal without config
    refute_match "ERROR", shell_output("#{bin}/idle-deck check 2>&1", 1)
  end

  # ---- Caveats ----
  def caveats
    <<~EOS
      idle-deck requires configuration before it can run. Set the following
      environment variables:

        export IDLE_DECK_GITHUB_TOKEN="your-fine-grained-pat"
        export IDLE_DECK_REPOS="owner/repo,owner/repo2"
        export IDLE_DECK_HARNESS_URL="https://your-harness.example.com"
        export IDLE_DECK_HARNESS_TOKEN="your-harness-token"

      Then run:
        idle-deck run

      For supervised execution on macOS, see the launchd plist in:
        #{opt_prefix}/docs/ops/launchd/com.seppaleinen.idle-deck.plist

      Documentation: https://github.com/seppaleinen/idle-deck
    EOS
  end
end