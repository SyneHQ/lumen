#!/usr/bin/env bash
#
# One-time setup: promote the local enterprise/ scaffold into a real private
# Git submodule.
#
# Run this ONCE, from the repository root, after creating an empty private
# repository at github.com/SyneHQ/lumen-enterprise.
#
# Until you run this, enterprise/ is a plain gitignored directory. The public
# repository builds fine either way — nothing in the open core depends on it.

set -euo pipefail

BRANCH="${ENTERPRISE_BRANCH:-main}"
DIR="enterprise"
ENTERPRISE_REPO="${ENTERPRISE_REPO:-lumen-enterprise}"

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarn:\033[0m %s\n' "$*" >&2; }

[[ -d .git ]] || die "run this from the repository root"
[[ -d "$DIR" ]] || die "$DIR/ not found — nothing to promote"
[[ -f "$DIR/go.mod" ]] || die "$DIR/go.mod not found — is this the right scaffold?"

if git config --file .gitmodules --get "submodule.$DIR.url" >/dev/null 2>&1; then
  die "$DIR is already a submodule; nothing to do"
fi

# ---------------------------------------------------------------------------
# Resolve the private remote URL, matching whatever protocol origin already
# uses (HTTPS or SSH) so this works without assuming an SSH key is set up.
# Override explicitly with ENTERPRISE_REMOTE=... if you want something else.
# ---------------------------------------------------------------------------
if [[ -n "${ENTERPRISE_REMOTE:-}" ]]; then
  REMOTE="$ENTERPRISE_REMOTE"
else
  ORIGIN="$(git remote get-url origin 2>/dev/null || true)"
  [[ -n "$ORIGIN" ]] || die "no 'origin' remote found; set ENTERPRISE_REMOTE=<url> explicitly"

  case "$ORIGIN" in
    # https://github.com/OWNER/repo(.git) → https://github.com/OWNER/lumen-enterprise.git
    http://*|https://*)
      BASE="${ORIGIN%.git}"
      REMOTE="${BASE%/*}/${ENTERPRISE_REPO}.git"
      ;;
    # git@github.com:OWNER/repo(.git) → git@github.com:OWNER/lumen-enterprise.git
    *:*)
      BASE="${ORIGIN%.git}"
      REMOTE="${BASE%/*}/${ENTERPRISE_REPO}.git"
      ;;
    *)
      die "could not parse origin URL '$ORIGIN'; set ENTERPRISE_REMOTE=<url> explicitly"
      ;;
  esac
  info "Derived private remote from origin ($ORIGIN)"
fi

info "Private remote: $REMOTE"

# ---------------------------------------------------------------------------
# Reachability. Over HTTPS a private repo returns 403/404 unless git can find
# a credential, so distinguish "does not exist" from "not authenticated".
# ---------------------------------------------------------------------------
info "Checking the private repository is reachable"
LS_ERR="$(git ls-remote "$REMOTE" 2>&1 >/dev/null || true)"
LS_OUT="$(git ls-remote "$REMOTE" 2>/dev/null || true)"

if [[ -n "$LS_ERR" ]]; then
  case "$LS_ERR" in
    *"could not read Username"*|*"Authentication failed"*|*"terminal prompts disabled"*)
      die "git could not authenticate to $REMOTE over HTTPS.

Set up a credential helper or use a token, then re-run:

  # macOS keychain (recommended)
  git config --global credential.helper osxkeychain

  # or authenticate via the GitHub CLI
  gh auth login && gh auth setup-git

  # or embed a PAT for this one call
  ENTERPRISE_REMOTE='https://<USER>:<TOKEN>@github.com/SyneHQ/${ENTERPRISE_REPO}.git' \\
    ./scripts/setup-enterprise.sh"
      ;;
    *"not found"*|*"Repository not found"*|*"does not exist"*)
      die "$REMOTE does not exist (or your account cannot see it).

Create the private repository first:

  gh repo create SyneHQ/${ENTERPRISE_REPO} --private
  # or via the web UI, with NO README and NO .gitignore (must be empty)"
      ;;
    *)
      die "cannot reach $REMOTE:

$LS_ERR"
      ;;
  esac
fi

if [[ -n "$LS_OUT" ]]; then
  die "$REMOTE is not empty. Refusing to overwrite.

If it already holds the enterprise code, add it as a submodule directly:
  rm -rf $DIR
  git submodule add --force -b $BRANCH '$REMOTE' $DIR"
fi

# Warn if the private repo looks public.
case "$REMOTE" in
  *github.com*)
    if command -v gh >/dev/null 2>&1; then
      SLUG="$(printf '%s' "${REMOTE%.git}" | sed -E 's#.*github\.com[:/]##')"
      VIS="$(gh repo view "$SLUG" --json visibility -q .visibility 2>/dev/null || true)"
      if [[ "$VIS" == "PUBLIC" ]]; then
        warn "$SLUG is PUBLIC. This repo holds proprietary code."
        warn "Make it private before continuing: gh repo edit $SLUG --visibility private"
        read -rp "Continue anyway? [y/N] " reply
        [[ "$reply" == "y" || "$reply" == "Y" ]] || die "aborted"
      fi
    fi
    ;;
esac

# ---------------------------------------------------------------------------
# 1. Push the scaffold to the private remote as its own repository
# ---------------------------------------------------------------------------
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT

info "Staging $DIR/ contents at $STAGING"
cp -R "$DIR/." "$STAGING/"
rm -rf "$STAGING/.git"

info "Creating initial commit in the private repository"
git -C "$STAGING" init -q -b "$BRANCH"
git -C "$STAGING" add -A
git -C "$STAGING" -c commit.gpgsign=false commit -q \
  -m "chore: initial enterprise module scaffold

Implements the open-source core's ee.Hooks interfaces:
license verification, per-tenant quota, usage metering, audit export."
git -C "$STAGING" remote add origin "$REMOTE"
git -C "$STAGING" push -q -u origin "$BRANCH"
info "Pushed to $REMOTE ($BRANCH)"

# ---------------------------------------------------------------------------
# 2. Replace the local directory with a submodule pointing at that remote
# ---------------------------------------------------------------------------
info "Removing local scaffold and re-adding as a submodule"
rm -rf "$DIR"

# enterprise/ is gitignored so the scaffold never leaks into the public repo.
# A submodule gitlink still needs --force to be added past that ignore rule.
git submodule add --force -b "$BRANCH" "$REMOTE" "$DIR"
git submodule update --init --recursive

cat <<'EOF'

Done.

  Public repo now tracks:  .gitmodules  +  enterprise (gitlink)
  Private code lives in:   SyneHQ/lumen-enterprise

Next:
  1. Drop 'enterprise/' from .gitignore (the gitlink replaces it).
  2. git add .gitmodules enterprise && git commit -m "build: add enterprise submodule"
  3. Confirm the open build is unaffected:
       git stash -u && go build ./... && go test -race ./... && git stash pop
  4. Build the commercial binary:
       cd enterprise && go build ./cmd/lumen-enterprise

Contributors without access to the private repo are unaffected: `git clone`
without --recursive leaves enterprise/ empty and the open build does not
reference it.
EOF
