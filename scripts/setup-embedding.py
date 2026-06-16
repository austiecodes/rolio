#!/usr/bin/env python3
"""Install pinned offline embedding artifacts. The service never downloads files."""

import hashlib
import json
import platform
import tempfile
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "crates/rolio-server/src/embedding/artifacts.json"
DESTINATION = ROOT / ".native/embedding/qwen3-embedding-0.6b"
MAX_DOWNLOAD_BYTES = 1024 * 1024 * 1024


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def download(url, path):
    if not url.startswith("https://"):
        raise ValueError("Artifact downloads require HTTPS")
    with urllib.request.urlopen(url, timeout=120) as response, path.open("wb") as output:
        size = 0
        while block := response.read(1024 * 1024):
            size += len(block)
            if size > MAX_DOWNLOAD_BYTES:
                raise ValueError("Artifact download exceeds the size limit")
            output.write(block)


def install(artifact):
    name = artifact["name"]
    if Path(name).name != name:
        raise ValueError("Artifact names must be plain file names")
    destination = DESTINATION / name
    if destination.is_file() and digest(destination) == artifact["sha256"]:
        print(f"Verified {destination}")
        return
    with tempfile.TemporaryDirectory(dir=DESTINATION) as temporary:
        download_path = Path(temporary) / "download"
        print(f"Downloading {name}", flush=True)
        download(artifact["url"], download_path)
        if digest(download_path) != artifact["sha256"]:
            raise ValueError(f"Artifact checksum mismatch for {name}")
        download_path.replace(destination)
        print(f"Installed {destination}")


def main():
    if (platform.system(), platform.machine()) != ("Darwin", "arm64"):
        raise SystemExit("Local embedding acceptance currently targets macOS ARM64 only")
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    DESTINATION.mkdir(parents=True, exist_ok=True)
    for artifact in manifest["files"]:
        install(artifact)


if __name__ == "__main__":
    main()
