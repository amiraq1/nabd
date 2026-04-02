import os
import shutil
from datetime import datetime
from typing import Any

from core.exceptions import ToolError


def backup_folder(
    source: str,
    destination_root: str,
    dry_run: bool = True,
) -> dict[str, Any]:
    if not os.path.isdir(source):
        raise ToolError(f"Source directory does not exist: {source}")

    folder_name = os.path.basename(source.rstrip(os.sep))
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    backup_name = f"{folder_name}_backup_{timestamp}"
    destination = os.path.join(destination_root, backup_name)

    file_count = 0
    total_size = 0
    for dirpath, _dirs, files in os.walk(source):
        for filename in files:
            fpath = os.path.join(dirpath, filename)
            try:
                total_size += os.path.getsize(fpath)
                file_count += 1
            except (OSError, PermissionError):
                pass

    from tools.utils import human_readable_size

    if dry_run:
        return {
            "source": source,
            "destination": destination,
            "dry_run": True,
            "file_count": file_count,
            "total_size_human": human_readable_size(total_size),
            "message": f"[DRY RUN] Would copy {file_count} files ({human_readable_size(total_size)}) to {destination}",
            "success": None,
            "errors": [],
        }

    errors: list[str] = []
    success = False

    try:
        shutil.copytree(source, destination)
        success = True
    except Exception as e:
        errors.append(str(e))

    return {
        "source": source,
        "destination": destination,
        "dry_run": False,
        "file_count": file_count,
        "total_size_human": human_readable_size(total_size),
        "success": success,
        "errors": errors,
    }
