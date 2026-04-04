import os
from typing import Any

from tools.utils import hash_file, human_readable_size, scan_files
from core.exceptions import ToolError

PREFIX_HASH_CHUNK_SIZE_KB = 4
PREFIX_HASH_MAX_CHUNKS = 1


def find_duplicates(
    directory: str, recursive: bool = True
) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    size_map: dict[int, list[str]] = {}
    for fpath in scan_files(directory, recursive=recursive):
        try:
            size = os.path.getsize(fpath)
            if size == 0:
                continue
            size_map.setdefault(size, []).append(fpath)
        except (OSError, PermissionError):
            pass

    candidates = {size: paths for size, paths in size_map.items() if len(paths) > 1}

    hash_map: dict[str, list[str]] = {}
    for paths in candidates.values():
        prefix_map: dict[str, list[str]] = {}
        for fpath in paths:
            prefix_digest = hash_file(
                fpath,
                chunk_size_kb=PREFIX_HASH_CHUNK_SIZE_KB,
                max_chunks=PREFIX_HASH_MAX_CHUNKS,
            )
            if prefix_digest:
                prefix_map.setdefault(prefix_digest, []).append(fpath)

        for prefix_paths in prefix_map.values():
            if len(prefix_paths) < 2:
                continue
            for fpath in prefix_paths:
                digest = hash_file(fpath)
                if digest:
                    hash_map.setdefault(digest, []).append(fpath)

    duplicate_groups: list[dict[str, Any]] = []
    total_wasted = 0

    for digest, paths in hash_map.items():
        if len(paths) < 2:
            continue
        try:
            file_size = os.path.getsize(paths[0])
        except (OSError, PermissionError):
            file_size = 0
        wasted = file_size * (len(paths) - 1)
        total_wasted += wasted
        duplicate_groups.append({
            "hash": digest,
            "file_size_human": human_readable_size(file_size),
            "paths": paths,
            "duplicate_count": len(paths) - 1,
            "wasted_space_human": human_readable_size(wasted),
        })

    duplicate_groups.sort(key=lambda g: -(g["duplicate_count"]))

    return {
        "directory": directory,
        "recursive": recursive,
        "duplicate_groups": duplicate_groups,
        "total_groups": len(duplicate_groups),
        "total_wasted_human": human_readable_size(total_wasted),
    }
