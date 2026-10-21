#!/bin/bash
# One-off recovery helper (deleted after use): extract pass-2 helper decls
# (referenced by the recovered handlers) verbatim from git HEAD.
set -u
cd "$(dirname "$0")"
OUT=/tmp/recovered2.go
{
  echo
  echo "// --- RECOVERED pass 2: helpers/types referenced by recovered handlers."
  echo "// --- Extracted verbatim from git HEAD; merge in place later."
  for n in unzipTo atoiDefault projStatus entrypointSummary configPushReq rbacAssignmentReq rbacScopeTypes systemRole quotaCheck projectQuotaLimits; do
    found=0
    for f in $(git ls-tree --name-only HEAD internal/api/ | grep '\.go$'); do
      ln=$(git show "HEAD:./$f" | grep -nE "^(func|type|var|const) [^{;=]*\b$n\b" | head -1 | cut -d: -f1)
      if [ -n "$ln" ]; then
        echo "// from ${f#internal/api/}"
        line=$(git show "HEAD:./$f" | sed -n "${ln}p")
        case "$line" in
          *"{") git show "HEAD:./$f" | sed -n "${ln},/^}/p" ;;
          *) echo "$line" ;;
        esac
        found=1
        break
      fi
    done
    if [ "$found" = 0 ]; then echo "// !! $n: no top-level decl found at HEAD"; fi
  done
} > "$OUT"
echo "decls extracted: $(grep -cE '^(func|type|var|const)' "$OUT")"
grep '!!' "$OUT" || true
