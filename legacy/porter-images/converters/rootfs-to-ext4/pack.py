#!/usr/bin/env python3
"""Merge an unpacked application rootfs onto a Porter base and pack rootfs.ext4.

Prototype: requires `mke2fs` (e2fsprogs) on the build host.
Production path is the Go converter in the main porter repo.
"""
import argparse
import os
import subprocess
import tempfile


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="path to unpacked base rootfs tree")
    ap.add_argument("--app", required=True, help="path to unpacked app rootfs tree")
    ap.add_argument("--out", required=True, help="output rootfs.ext4 path")
    ap.add_argument("--size-gb", type=int, default=2)
    args = ap.parse_args()

    with tempfile.TemporaryDirectory() as merged:
        # overlay: base first, app on top (app wins conflicts)
        subprocess.run(["cp", "-a", f"{args.base}/.", merged], check=True)
        subprocess.run(["cp", "-aT", args.app, merged], check=True)

        size = f"{args.size_gb}G"
        subprocess.run(["truncate", "-s", size, args.out], check=True)
        subprocess.run(
            ["mke2fs", "-t", "ext4", "-d", merged, "-F", args.out],
            check=True,
        )
    print(f"rootfs written: {args.out}")


if __name__ == "__main__":
    main()
