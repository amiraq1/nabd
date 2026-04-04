import os
from typing import Any

from tools.utils import get_category, human_readable_size, scan_files
from core.exceptions import ToolError


def get_storage_report(directory: str) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    total_size = 0
    file_count = 0
    dir_count = 0
    category_sizes: dict[str, int] = {}

    for dirpath, dirs, files in os.walk(directory):
        dir_count += len(dirs)
        for filename in files:
            fpath = os.path.join(dirpath, filename)
            try:
                size = os.path.getsize(fpath)
                total_size += size
                file_count += 1
                ext = os.path.splitext(filename)[1].lower()
                cat = get_category(ext)
                category_sizes[cat] = category_sizes.get(cat, 0) + size
            except (OSError, PermissionError):
                pass

    disk = os.statvfs(directory) if hasattr(os, "statvfs") else None
    free_bytes = disk.f_bavail * disk.f_frsize if disk else None

    return {
        "directory": directory,
        "total_size_bytes": total_size,
        "total_size_human": human_readable_size(total_size),
        "file_count": file_count,
        "directory_count": dir_count,
        "free_space_human": human_readable_size(free_bytes) if free_bytes is not None else "N/A",
        "category_breakdown": {
            cat: human_readable_size(sz)
            for cat, sz in sorted(category_sizes.items(), key=lambda x: -x[1])
        },
    }


def list_large_files(
    directory: str,
    top_n: int = 20,
    threshold_mb: float = 0.0,
) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    threshold_bytes = int(threshold_mb * 1024 * 1024)
    files_info: list[dict[str, Any]] = []

    for fpath in scan_files(directory, recursive=True):
        try:
            size = os.path.getsize(fpath)
            if size >= threshold_bytes:
                files_info.append({
                    "path": fpath,
                    "size_bytes": size,
                    "size_human": human_readable_size(size),
                })
        except (OSError, PermissionError):
            pass

    files_info.sort(key=lambda x: -x["size_bytes"])
    return {
        "directory": directory,
        "files": files_info[:top_n],
    }
