#!/usr/bin/env python3
"""Write the versioned artifact contract consumed by the installer."""
import hashlib
import json
import pathlib
import re
import sys


def manifest(version: str, architecture: str, directory: pathlib.Path) -> dict:
    if architecture not in {"amd64", "arm64"} or not re.fullmatch(r"[A-Za-z0-9.+~-]+", version):
        raise ValueError("invalid package version or architecture")
    filename = f"urmail_{version}_{architecture}.deb"
    data = (directory / filename).read_bytes()
    return {"version": version, "architecture": architecture, "filename": filename,
            "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}


if __name__ == "__main__":
    destination = pathlib.Path(sys.argv[3])
    result = manifest(sys.argv[1], sys.argv[2], destination)
    (destination / f"manifest-{sys.argv[2]}.json").write_text(json.dumps(result, indent=2) + "\n")
    (destination / (result["filename"] + ".sha256")).write_text(result["sha256"] + "  " + result["filename"] + "\n")
