import os
import shutil
from typing import Any

from tools.utils import get_category, scan_files, unique_dest_path
from core.exceptions import ToolError


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
