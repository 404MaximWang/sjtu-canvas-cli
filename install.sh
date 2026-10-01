#!/bin/sh
set -e

main() {
  REPO="404MaximWang/sjtu-canvas-cli"

  # Welcome
  echo "Welcome to use sjtu-canvas-cli!"
  echo "少年出城北，婉唱鸟鸣悲。"
  # Detect OS
  OS="$(uname -s)"
  case "$OS" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)
      echo "Error: Unsupported operating system: $OS" >&2
      exit 1
      ;;
  esac

  # Detect Architecture
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
      echo "Error: Unsupported architecture: $ARCH" >&2
      exit 1
      ;;
  esac

  echo "Detected OS: $OS, Architecture: $ARCH"

  # Dynamically resolve latest tag or respect specified VERSION
  if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
    echo "Fetching latest version tag..."
    LATEST_URL="$(curl -fsSLI -o /dev/null -w "%{url_effective}" "https://github.com/${REPO}/releases/latest")"
    TAG="${LATEST_URL##*/}"
    if [ -z "$TAG" ] || [ "$TAG" = "releases" ] || [ "$TAG" = "latest" ]; then
      echo "Error: Failed to determine latest release tag from GitHub." >&2
      exit 1
    fi
  else
    TAG="$VERSION"
  fi

  TARBALL="sjtu-${TAG}-${OS}-${ARCH}.tar.gz"
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TAG}/${TARBALL}"
  CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"

  # Create temporary directory for download and extraction
  TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t 'sjtu-install')"
  trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

  echo "Downloading ${TARBALL} (${TAG})..."
  curl -fsSL "$DOWNLOAD_URL" -o "${TMP_DIR}/${TARBALL}"
  curl -fsSL "$CHECKSUMS_URL" -o "${TMP_DIR}/checksums.txt"

  echo "Verifying checksum..."
  EXPECTED_HASH="$(grep "  ${TARBALL}$" "${TMP_DIR}/checksums.txt" | awk '{print $1}')"
  if [ -z "$EXPECTED_HASH" ]; then
    echo "Error: Checksum for ${TARBALL} not found in checksums.txt" >&2
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_HASH="$(sha256sum "${TMP_DIR}/${TARBALL}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_HASH="$(shasum -a 256 "${TMP_DIR}/${TARBALL}" | awk '{print $1}')"
  else
    echo "Warning: Neither sha256sum nor shasum found; skipping checksum verification."
    ACTUAL_HASH="$EXPECTED_HASH"
  fi

  if [ "$EXPECTED_HASH" != "$ACTUAL_HASH" ]; then
    echo "Error: Checksum mismatch! Expected ${EXPECTED_HASH}, got ${ACTUAL_HASH}" >&2
    exit 1
  fi

  INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$INSTALL_DIR"

  echo "Extracting binary..."
  tar -xzf "${TMP_DIR}/${TARBALL}" -C "${TMP_DIR}"
  mv "${TMP_DIR}/sjtu" "${INSTALL_DIR}/sjtu"
  chmod +x "${INSTALL_DIR}/sjtu"

  echo "Successfully installed sjtu (${TAG}) to ${INSTALL_DIR}/sjtu"

  case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      echo ""
      echo "Note: ${INSTALL_DIR} is not in your PATH."
      echo "Add the following line to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
      echo "    export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac
}

main "$@"
