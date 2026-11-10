#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' 'Disabled: this legacy E2E used a shared HTTP test directory, broad process-kill cleanup, and incomplete restore assertions.' >&2
printf '%s\n' 'See scripts/README.md and use the opt-in KVM acceptance lane instead.' >&2
exit 2
