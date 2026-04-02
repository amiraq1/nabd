import os
import shutil
from typing import Any

from tools.utils import get_category, human_readable_size, scan_files, unique_dest_path
from core.exceptions import ToolError

MEDIA_CATEGORIES = {"images", "videos", "audio"}


def organize_folder_by_type(
    directory: str, dry_run: bool = True
) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    planned: list[dict[str, str]] = []
    skipped: list[str] = []
    errors: list[str] = []

    for fpath in scan_files(directory, recursive=False):
        filename = os.path.basename(fpath)
        ext = os.path.splitext(filename)[1]
        category = get_category(ext)
        dest_dir = os.path.join(directory, category)
        dest_path = os.path.join(dest_dir, filename)

        if os.path.normcase(os.path.abspath(fpath)) == os.path.normcase(os.path.abspath(dest_path)):
            skipped.append(fpath)
            continue

        if os.path.exists(dest_path):
            dest_path = unique_dest_path(dest_path)

        planned.append({"source": fpath, "destination": dest_path, "category": category})

    moved: list[str] = []
    if not dry_run:
        for item in planned:
            try:
                os.makedirs(os.path.dirname(item["destination"]), exist_ok=True)
                dest = item["destination"]
                if os.path.exists(dest):
                    dest = unique_dest_path(dest)
                shutil.move(item["source"], dest)
                moved.append(dest)
            except Exception as e:
                errors.append(f"Failed to move '{os.path.basename(item['source'])}': {e}")

    return {
        "directory": directory,
        "dry_run": dry_run,
        "planned_moves": planned,
        "moved": moved,
        "skipped": skipped,
        "errors": errors,
    }


def safe_rename_files(
    directory: str,
    prefix: str = "",
    suffix: str = "",
    dry_run: bool = True,
) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")
    if not prefix and not suffix:
        raise ToolError("At least one of prefix or suffix must be provided.")

    planned: list[dict[str, str]] = []
    errors: list[str] = []

    for fpath in scan_files(directory, recursive=False):
        filename = os.path.basename(fpath)
        base, ext = os.path.splitext(filename)
        new_name = f"{prefix}{base}{suffix}{ext}"
        dest = os.path.join(directory, new_name)
        if os.path.normcase(fpath) == os.path.normcase(dest):
            continue
        planned.append({"source": fpath, "destination": dest})

    renamed: list[str] = []
    if not dry_run:
        for item in planned:
            try:
                if os.path.exists(item["destination"]):
                    errors.append(
                        f"Target already exists, skipping: '{os.path.basename(item['destination'])}'"
                    )
                    continue
                os.rename(item["source"], item["destination"])
                renamed.append(item["destination"])
            except Exception as e:
                errors.append(f"Failed to rename '{os.path.basename(item['source'])}': {e}")

    return {
        "directory": directory,
        "dry_run": dry_run,
        "planned_renames": planned,
        "renamed": renamed,
        "errors": errors,
    }


def safe_move_files(
    source_path: str,
    target_directory: str,
    dry_run: bool = True,
) -> dict[str, Any]:
    if not os.path.exists(source_path):
        raise ToolError(f"Source path does not exist: {source_path}")

    filename = os.path.basename(source_path.rstrip(os.sep)) or os.path.basename(source_path)
    dest_path = os.path.join(target_directory, filename)
    if os.path.exists(dest_path):
        dest_path = unique_dest_path(dest_path)

    planned = {"source": source_path, "destination": dest_path}
    moved: list[str] = []
    errors: list[str] = []

    if not dry_run:
        try:
            os.makedirs(target_directory, exist_ok=True)
            final_dest = dest_path
            if os.path.exists(final_dest):
                final_dest = unique_dest_path(final_dest)
            shutil.move(source_path, final_dest)
            moved.append(final_dest)
        except Exception as e:
            errors.append(f"Failed to move '{filename}': {e}")

    return {
        "source": source_path,
        "target_directory": target_directory,
        "dry_run": dry_run,
        "planned": planned,
        "moved": moved,
        "errors": errors,
    }


def show_files(
    directory: str,
    sort_by: str = "name",
    limit: int = 100,
) -> dict[str, Any]:
    """
    List files in a directory with name, size, and last-modified time.
    sort_by: 'name' | 'size' | 'modified'
    """
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    entries: list[dict[str, Any]] = []
    errors: list[str] = []

    try:
        for entry in os.scandir(directory):
            try:
                stat = entry.stat(follow_symlinks=False)
                is_dir = entry.is_dir(follow_symlinks=False)
                entries.append({
                    "name": entry.name,
                    "path": entry.path,
                    "is_dir": is_dir,
                    "size_bytes": stat.st_size if not is_dir else 0,
                    "size_human": human_readable_size(stat.st_size) if not is_dir else "—",
                    "modified_ts": stat.st_mtime,
                    "ext": os.path.splitext(entry.name)[1].lower() if not is_dir else "",
                })
            except (OSError, PermissionError) as e:
                errors.append(str(e))
    except (OSError, PermissionError) as e:
        raise ToolError(f"Cannot read directory '{directory}': {e}")

    if sort_by == "size":
        entries.sort(key=lambda x: -x["size_bytes"])
    elif sort_by == "modified":
        entries.sort(key=lambda x: -x["modified_ts"])
    else:
        entries.sort(key=lambda x: (x["is_dir"] is False, x["name"].lower()))

    dirs = [e for e in entries if e["is_dir"]]
    files = [e for e in entries if not e["is_dir"]]

    return {
        "directory": directory,
        "total_entries": len(entries),
        "file_count": len(files),
        "dir_count": len(dirs),
        "entries": entries[:limit],
        "truncated": max(0, len(entries) - limit),
        "sort_by": sort_by,
        "errors": errors,
    }


def list_media(
    directory: str,
    recursive: bool = False,
) -> dict[str, Any]:
    """
    List all media files (images, videos, audio) in a directory.
    Groups results by category and returns per-category counts and sizes.
    """
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    groups: dict[str, list[dict[str, Any]]] = {
        "images": [],
        "videos": [],
        "audio": [],
    }
    errors: list[str] = []
    total_size = 0

    for fpath in scan_files(directory, recursive=recursive):
        try:
            ext = os.path.splitext(fpath)[1].lower()
            cat = get_category(ext)
            if cat not in MEDIA_CATEGORIES:
                continue
            size = os.path.getsize(fpath)
            total_size += size
            groups[cat].append({
                "path": fpath,
                "name": os.path.basename(fpath),
                "size_bytes": size,
                "size_human": human_readable_size(size),
                "ext": ext,
            })
        except (OSError, PermissionError) as e:
            errors.append(str(e))

    summary: dict[str, Any] = {}
    for cat, items in groups.items():
        items.sort(key=lambda x: x["name"].lower())
        cat_size = sum(i["size_bytes"] for i in items)
        summary[cat] = {
            "count": len(items),
            "total_size_human": human_readable_size(cat_size),
            "total_size_bytes": cat_size,
        }

    total_count = sum(len(v) for v in groups.values())

    return {
        "directory": directory,
        "recursive": recursive,
        "total_media_count": total_count,
        "total_size_human": human_readable_size(total_size),
        "total_size_bytes": total_size,
        "groups": groups,
        "summary": summary,
        "errors": errors,
    }
