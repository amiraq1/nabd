import hashlib
import os
from typing import Generator

from core.exceptions import ToolError

MAX_UNIQUE_DEST_ATTEMPTS = 10_000


EXTENSION_CATEGORIES: dict[str, set[str]] = {
    "images": {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".heic", ".tiff", ".svg"},
    "videos": {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".3gp", ".m4v"},
    "audio": {".mp3", ".wav", ".aac", ".flac", ".ogg", ".m4a", ".opus", ".wma"},
    "documents": {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt",
                  ".odt", ".ods", ".odp", ".rtf", ".csv", ".md"},
    "archives": {".zip", ".tar", ".gz", ".bz2", ".xz", ".rar", ".7z", ".tgz"},
    "code": {".py", ".js", ".ts", ".java", ".c", ".cpp", ".h", ".go", ".rs",
             ".sh", ".html", ".css", ".json", ".xml", ".yaml", ".yml"},
    "apks": {".apk"},
}


def get_category(ext: str) -> str:
    ext_lower = ext.lower()
    for category, extensions in EXTENSION_CATEGORIES.items():
        if ext_lower in extensions:
            return category
    return "other"


def human_readable_size(size_bytes: int) -> str:
    if size_bytes < 1024:
        return f"{size_bytes} B"
    elif size_bytes < 1024 ** 2:
        return f"{size_bytes / 1024:.1f} KB"
    elif size_bytes < 1024 ** 3:
        return f"{size_bytes / 1024 ** 2:.1f} MB"
    else:
        return f"{size_bytes / 1024 ** 3:.2f} GB"


def scan_files(directory: str, recursive: bool = False) -> Generator[str, None, None]:
    if recursive:
        for dirpath, _dirs, files in os.walk(directory):
            for filename in files:
                fpath = os.path.join(dirpath, filename)
                try:
                    yield fpath
                except Exception:
                    pass
    else:
        try:
            for entry in os.scandir(directory):
                try:
                    if entry.is_file(follow_symlinks=False):
                        yield entry.path
                except (OSError, PermissionError):
                    pass
        except (OSError, PermissionError):
            return


def hash_file(
    filepath: str,
    chunk_size_kb: int = 64,
    max_chunks: int | None = None,
) -> str:
    sha256 = hashlib.sha256()
    chunk_size = chunk_size_kb * 1024
    try:
        with open(filepath, "rb") as f:
            chunks_read = 0
            while True:
                if max_chunks is not None and chunks_read >= max_chunks:
                    break
                chunk = f.read(chunk_size)
                if not chunk:
                    break
                sha256.update(chunk)
                chunks_read += 1
        return sha256.hexdigest()
    except (OSError, PermissionError):
        return ""


def safe_filename(name: str) -> str:
    forbidden = '/\\:*?"<>|'
    result = "".join(c if c not in forbidden else "_" for c in name)
    return result.strip(". ") or "unnamed"


def truncate_list(items: list, max_items: int = 10) -> tuple[list, int]:
    if len(items) <= max_items:
        return items, 0
    return items[:max_items], len(items) - max_items


def unique_dest_path(dest_path: str) -> str:
    """Return dest_path, or dest_path with an incrementing counter suffix if it already exists."""
    if not os.path.exists(dest_path):
        return dest_path
    base, ext = os.path.splitext(dest_path)
    counter = 1
    while counter <= MAX_UNIQUE_DEST_ATTEMPTS:
        candidate = f"{base}_{counter}{ext}"
        if not os.path.exists(candidate):
            return candidate
        counter += 1
    raise ToolError(
        f"Could not find an available destination path after "
        f"{MAX_UNIQUE_DEST_ATTEMPTS} attempts: {dest_path}"
    )
