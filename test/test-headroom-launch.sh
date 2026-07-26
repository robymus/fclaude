#!/usr/bin/env bash
# Unit tests for the pure Headroom launch helpers in lib/common.sh
# (_headroom_launch / _headroom_project_label / _tokensave_sync_cmd).
# No VM or network needed.
#
# The point of these: the wrappers deliberately do NOT use `headroom wrap`,
# because wrap writes ANTHROPIC_BASE_URL into <cwd>/.claude/settings.local.json
# — which, over the NFS mount, is the user's real host repo. So the launch line
# must carry that configuration itself, and must never grow a `wrap` back.
#
# Run:  bash test/test-headroom-launch.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../lib/common.sh
source "${REPO_DIR}/lib/common.sh"

pass=0 fail=0

# has <description> <expected-substring> <actual>
has() {
  local desc="$1" want="$2" got="$3"
  if [[ "$got" == *"$want"* ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  wanted substring: %s\n  got:              %s\n' "$desc" "$want" "$got"
  fi
}

# hasnt <description> <forbidden-substring> <actual>
hasnt() {
  local desc="$1" bad="$2" got="$3"
  if [[ "$got" != *"$bad"* ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  must NOT contain: %s\n  got:              %s\n' "$desc" "$bad" "$got"
  fi
}

# eq <description> <expected> <actual>
eq() {
  local desc="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  wanted: %s\n  got:    %s\n' "$desc" "$want" "$got"
  fi
}

# --- _headroom_project_label ----------------------------------------------
# Mirrors headroom's urllib.parse.quote(name, safe="-_.() ").
eq "label passes through plain names" "fclaude" "$(_headroom_project_label fclaude)"
eq "label keeps the safe set" "a-b_c.d~e(f) g" "$(_headroom_project_label 'a-b_c.d~e(f) g')"
eq "label encodes a slash" "a%2Fb" "$(_headroom_project_label 'a/b')"
eq "label encodes a colon (header separator)" "a%3Ab" "$(_headroom_project_label 'a:b')"
eq "label encodes a newline (header injection)" "a%0Ab" "$(_headroom_project_label 'a
b')"
eq "label of an empty name is empty" "" "$(_headroom_project_label '')"

# --- _headroom_launch -----------------------------------------------------
export FCLAUDE_HEADROOM_ENABLE=1
export FCLAUDE_HEADROOM_PORT=8787
export FCLAUDE_HEADROOM_MODEL=claude-opus-5
out="$(_headroom_launch /home/user/projects/myrepo)"

has  "launch runs claude via env"            "env " "$out"
has  "launch ends by invoking claude"        " claude --settings" "$out"
has  "launch sets the proxy base url"        "ANTHROPIC_BASE_URL=http://127.0.0.1:8787" "$out"
has  "launch keeps tool deferral on (#746)"  "ENABLE_TOOL_SEARCH=true" "$out"
has  "launch pins the 1M model suffix"       'ANTHROPIC_MODEL=claude-opus-5\[1m\]' "$out"
has  "launch tags the project for savings"   "X-Headroom-Project:" "$out"
has  "launch labels the workspace basename"  "myrepo" "$out"
# The whole point of this module: never route through `headroom wrap` again.
hasnt "launch never calls headroom wrap"     "wrap" "$out"
hasnt "launch never calls the removed shim"  "headroom-quiet" "$out"

# The emitted string is consumed by a REMOTE shell, so every word must survive
# one round of shell parsing — including the [1m] brackets, the space inside the
# header value, and the JSON punctuation. Assert on the PARSED words: the raw
# string is %q-escaped by design, so matching it verbatim would prove nothing.
mapfile -t words < <(eval "printf '%s\n' $out")
eq "launch quoting survives shell parsing: model" \
  "ANTHROPIC_MODEL=claude-opus-5[1m]" "${words[3]}"
eq "launch quoting survives shell parsing: header" \
  "ANTHROPIC_CUSTOM_HEADERS=X-Headroom-Project: myrepo" "${words[4]}"
# The base url must ALSO reach cc-daemon's conversation workers, which re-read
# settings instead of inheriting the environment (headroom #951). That is the
# only reason wrap wrote a settings file; inline --settings JSON replaces it.
eq "launch passes the base url as inline settings JSON" \
  '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}' "${words[7]}"

# --- port / model / enable knobs ------------------------------------------
FCLAUDE_HEADROOM_PORT=9999
has "launch honours a custom port" "http://127.0.0.1:9999" "$(_headroom_launch /tmp/x)"
FCLAUDE_HEADROOM_PORT=8787

FCLAUDE_HEADROOM_MODEL=""
hasnt "empty model sets no ANTHROPIC_MODEL" "ANTHROPIC_MODEL" "$(_headroom_launch /tmp/x)"
FCLAUDE_HEADROOM_MODEL=claude-opus-5

FCLAUDE_HEADROOM_ENABLE=0
eq "disabled headroom launches plain claude" "claude" "$(_headroom_launch /tmp/x)"
FCLAUDE_HEADROOM_ENABLE=1

# --- _tokensave_sync_cmd --------------------------------------------------
sync_out="$(_tokensave_sync_cmd)"
has "sync incrementally updates an existing graph" "tokensave sync" "$sync_out"
has "sync initializes a repo with no graph yet"    "tokensave init" "$sync_out"
has "sync is skipped when tokensave is absent"     "command -v tokensave" "$sync_out"
has "sync is silent (stdio JSON protocol safety)"  ">/dev/null 2>&1" "$sync_out"
has "sync never fails the launch"                  "|| true" "$sync_out"
has "sync is terminated so a command can follow"   ";" "${sync_out: -1};"
# Must parse as a complete compound command in the remote shell.
if bash -n <<<"$sync_out true"; then pass=$((pass + 1)); else
  fail=$((fail + 1)); printf 'FAIL: tokensave snippet is not valid shell: %s\n' "$sync_out"
fi

# Last knob in the file, so it is not restored afterwards.
export FCLAUDE_TOKENSAVE_SYNC=0
eq "sync emits nothing when disabled" "" "$(_tokensave_sync_cmd)"

# --- summary --------------------------------------------------------------
printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
