#!/usr/bin/env python3
"""Validate all images/*/image.json against the schema and rebuild catalog.json."""
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]

try:
    import jsonschema
except ImportError:
    print("pip install jsonschema", file=sys.stderr)
    sys.exit(1)

errors = 0
catalog = []
schema = json.loads((ROOT / "schema" / "image.schema.json").read_text())
validator = jsonschema.Draft202012Validator(schema)

for path in sorted((ROOT / "images").glob("*/image.json")):
    data = json.loads(path.read_text())
    for err in validator.iter_errors(data):
        errors += 1
        print(f"ERROR {path}: {err.json_path} {err.message}", file=sys.stderr)
    name = data["name"]
    if name != path.parent.name:
        errors += 1
        print(f"ERROR {path}: name '{name}' != directory '{path.parent.name}'", file=sys.stderr)
    defaults = data.get("defaults", {})
    src_kind = data["source"]["kind"]
    src_ref = data["source"]["reference"]
    if src_kind == "github-release":
        src_label = f"github.com/{src_ref} (release: {{tag}})"
    elif src_kind in ("dockerfile", "compose"):
        src_label = f"build from {src_ref}"
    else:
        src_label = f"{src_ref}:{{tag}}"
    for v in data["versions"]:
        res = {**defaults, **v.get("overrides", {})}
        catalog.append({
            "name": name,
            "title": data.get("title", name),
            "description": data.get("description", ""),
            "type": data.get("type", "application"),
            "official": data.get("official", True),
            "arch": data.get("arch", ["x86_64"]),
            "category": data.get("category", "other"),
            "source": src_label.replace("{tag}", v["tag"]),
            "source": src_label,
            "tag": v["tag"],
            "base": data["base"]["ref"] if "base" in data else None,
            "status": v.get("status", "stable"),
            "resources": {k: res[k] for k in ("cpu", "ramMb", "diskGb") if k in res},
            "ports": res.get("ports", []),
            "volumes": res.get("volumes", []),
            "health": res.get("health"),
            "install": f"porter image install {name}:{v['tag']}",
        })

if errors:
    sys.exit(1)

(ROOT / "catalog.json").write_text(json.dumps({"schemaVersion": 1, "images": catalog}, indent=2) + "\n")
print(f"OK: {len(catalog)} image versions validated, catalog.json rebuilt")
