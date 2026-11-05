#!/bin/sh
set -e

# Git identity
if [ -n "$GIT_AUTHOR_NAME" ]; then
  git config --global user.name "$GIT_AUTHOR_NAME"
  git config --global user.email "$GIT_AUTHOR_EMAIL"
fi

# SSH commit signing
if [ -f "$GIT_SIGNING_KEY_PATH" ]; then
  mkdir -p ~/.ssh
  cp "$GIT_SIGNING_KEY_PATH" ~/.ssh/git_signing
  cp "$GIT_SIGNING_PUBKEY_PATH" ~/.ssh/git_signing.pub
  chmod 600 ~/.ssh/git_signing
  chmod 644 ~/.ssh/git_signing.pub

  git config --global gpg.format ssh
  git config --global user.signingkey ~/.ssh/git_signing.pub
  git config --global commit.gpgsign true
  git config --global tag.gpgsign true

  cat > ~/.ssh/allowed_signers <<EOF
$GIT_AUTHOR_EMAIL $(cat ~/.ssh/git_signing.pub)
EOF
  git config --global gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers
fi

# GitHub CLI auth
if [ -n "$GITHUB_TOKEN" ]; then
  echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
  git config --global url."https://oauth2:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"
fi

# Tailscale
if [ -n "$TS_AUTHKEY" ]; then
  tailscaled --tun=userspace-networking --state=/workspace/.tailscale &
  sleep 2
  tailscale up --authkey="$TS_AUTHKEY" --accept-routes --hostname=claude-remote
fi

exec claude-remote "$@"
