#!/usr/bin/env bash
# Start MicroVM Maker (as your normal user, never root).
cd "$(dirname "$0")"
PY=python3; [ -x .venv/bin/python ] && PY=.venv/bin/python
exec "$PY" -u app.py "$@"
