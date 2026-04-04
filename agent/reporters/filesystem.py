from typing import Any

from tools.utils import truncate_list

from .registry import register_raw_detail


@register_raw_detail("doctor")
def render_doctor(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    checks = raw.get("checks", [])
    ok = raw.get("ok_count", 0)
    warn = raw.get("warn_count", 0)
    err = raw.get("error_count", 0)
    overall = raw.get("overall", "?")

    icon_map = {"ok": "✓", "warn": "⚠", "missing": "✗", "error": "✗"}
    lines.append("")
    for check in checks:
        icon = icon_map.get(check["status"], "?")
        lines.append(f"  {icon}  {check['name']:<30} {check['detail']}")

    lines.append("")
    summary_parts = [f"{ok} ok"]
    if warn:
        summary_parts.append(f"{warn} warning{'s' if warn != 1 else ''}")
    if err:
        summary_parts.append(f"{err} issue{'s' if err != 1 else ''}")
    overall_label = {"ok": "All checks passed.", "warn": "Ready with warnings.", "error": "Action required."}.get(overall, "")
    lines.append(f"  Summary: {', '.join(summary_parts)}. {overall_label}")


@register_raw_detail("storage_report")
def render_storage_report(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    lines.append(f"\n  Directory    : {raw.get('directory', '')}")
    lines.append(f"  Total Size   : {raw.get('total_size_human', '')}")
    lines.append(f"  Files        : {raw.get('file_count', 0)}")
    lines.append(f"  Directories  : {raw.get('directory_count', 0)}")
    lines.append(f"  Free Space   : {raw.get('free_space_human', 'N/A')}")
    breakdown = raw.get("category_breakdown", {})
    if breakdown:
        lines.append("  By Category  :")
        for cat, size in breakdown.items():
            lines.append(f"    • {cat:<14}: {size}")


@register_raw_detail("list_large_files")
def render_list_large_files(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    files = raw if isinstance(raw, list) else raw.get("files", [])
    lines.append(f"\n  Found {len(files)} large file(s):")
    shown, extra = truncate_list(files, 20)
    for item in shown:
        if isinstance(item, dict):
            lines.append(f"    {item.get('size_human', '?'):>10}  {item.get('path', '')}")
    if extra:
        lines.append(f"    ... and {extra} more")


@register_raw_detail("show_files")
def render_show_files(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    entries = raw.get("entries", [])
    file_count = raw.get("file_count", 0)
    dir_count = raw.get("dir_count", 0)
    truncated = raw.get("truncated", 0)
    sort_by = raw.get("sort_by", "name")

    lines.append(f"\n  Directory : {raw.get('directory', '')}")
    lines.append(f"  Contents  : {file_count} file(s), {dir_count} folder(s)  (sorted by {sort_by})")
    lines.append("")

    for entry in entries:
        if entry["is_dir"]:
            lines.append(f"    {'DIR':>10}  📁 {entry['name']}/")
        else:
            lines.append(f"    {entry['size_human']:>10}  {entry['name']}")

    if truncated:
        lines.append(f"\n    ... and {truncated} more entries (use 'limit N' to see more)")


@register_raw_detail("list_media")
def render_list_media(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    summary = raw.get("summary", {})
    groups = raw.get("groups", {})
    total = raw.get("total_media_count", 0)
    total_size = raw.get("total_size_human", "0 B")
    directory = raw.get("directory", "")
    recursive = raw.get("recursive", False)

    lines.append(f"\n  Directory : {directory}{'  (recursive)' if recursive else ''}")
    lines.append(f"  Total     : {total} media file(s), {total_size}")
    lines.append("")

    category_labels = {"images": "Images", "videos": "Videos", "audio": "Audio"}
    for category, label in category_labels.items():
        info = summary.get(category, {})
        count = info.get("count", 0)
        size = info.get("total_size_human", "0 B")
        items = groups.get(category, [])
        if count == 0:
            lines.append(f"  {label:<8}: (none)")
            continue
        lines.append(f"  {label:<8}: {count} file(s), {size}")
        shown, extra = truncate_list(items, 10)
        for item in shown:
            lines.append(f"    {item['size_human']:>10}  {item['name']}")
        if extra:
            lines.append(f"    ... and {extra} more")

    if total == 0 and not recursive and raw.get("has_subdirs"):
        lines.append(
            f"\n  Hint: No media found directly in this folder, but subfolders exist."
            f"\n        To scan them too, try:"
            f"\n          list media in {directory} recursively"
        )


@register_raw_detail("show_folders")
def render_show_folders(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    directory = raw.get("directory", "")
    folder_count = raw.get("folder_count", 0)
    folders = raw.get("folders", [])
    errors = raw.get("errors", [])
    lines.append(f"\n  Directory  : {directory}")
    lines.append(f"  Subfolders : {folder_count}")
    if folder_count == 0:
        lines.append("\n  No subfolders found.")
    else:
        lines.append("")
        shown, extra = truncate_list(folders, 20)
        for folder in shown:
            name = folder.get("name", "")
            count = folder.get("item_count")
            count_str = f"({count} item{'s' if count != 1 else ''})" if count is not None else "(unreadable)"
            lines.append(f"    {name + '/':<30} {count_str}")
        if extra:
            lines.append(f"    ... and {extra} more")
    if errors:
        lines.append("\n  Errors:")
        for error in errors:
            lines.append(f"    ! {error}")


@register_raw_detail("organize_folder_by_type")
def render_organize_folder_by_type(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    moves = raw.get("planned_moves", [])
    skipped = raw.get("skipped", [])
    moved = raw.get("moved", [])
    errors = raw.get("errors", [])
    if confirmed:
        lines.append(f"\n  Moved  : {len(moved)} file(s)")
        lines.append(f"  Errors : {len(errors)}")
        return

    lines.append(f"\n  Would move  : {len(moves)} file(s)")
    lines.append(f"  Would skip  : {len(skipped)} (already in correct folder)")
    shown, extra = truncate_list(moves, 10)
    for move in shown:
        src_name = move.get("source", "").split("/")[-1]
        dest_dir = "/".join(move.get("destination", "").split("/")[:-1]).split("/")[-1]
        lines.append(f"    {src_name}  →  {dest_dir}/")
    if extra:
        lines.append(f"    ... and {extra} more")


@register_raw_detail("find_duplicates")
def render_find_duplicates(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    groups = raw.get("duplicate_groups", [])
    total_wasted = raw.get("total_wasted_human", "0 B")
    total_groups = raw.get("total_groups", 0)
    total_files = sum(len(group.get("paths", [])) for group in groups)

    lines.append(f"\n  Duplicate groups : {total_groups}")
    lines.append(f"  Duplicate files  : {total_files}")
    lines.append(f"  Wasted space     : {total_wasted}")

    if total_groups == 0:
        lines.append("\n  No duplicates found.")
        return

    shown_groups, extra_groups = truncate_list(groups, 5)
    lines.append("")
    for i, group in enumerate(shown_groups, 1):
        size_human = group.get("file_size_human", "?")
        paths = group.get("paths", [])
        lines.append(f"  Group {i}  ({size_human} × {len(paths)} copies):")
        shown_paths, extra_paths = truncate_list(paths, 4)
        for path in shown_paths:
            lines.append(f"    • {path}")
        if extra_paths:
            lines.append(f"    ... and {extra_paths} more copies")

    if extra_groups:
        lines.append(f"\n  ... and {extra_groups} more group(s) not shown.")
        lines.append("  Tip: use 'storage report' to see space usage by category.")
