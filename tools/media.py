import os
import shutil
import subprocess
import tempfile
from typing import Any

from core.exceptions import ToolError

VALID_VIDEO_EXTS = {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".3gp", ".m4v"}
COMPRESSIBLE_IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp"}


def _check_ffmpeg() -> bool:
    return shutil.which("ffmpeg") is not None


def _check_pillow() -> bool:
    try:
        import PIL  # noqa: F401
        return True
    except ImportError:
        return False


def convert_video_to_mp3(
    video_path: str,
    output_path: str,
    dry_run: bool = True,
) -> dict[str, Any]:
    if not os.path.isfile(video_path):
        raise ToolError(f"Video file does not exist: {video_path}")

    ext = os.path.splitext(video_path)[1].lower()
    if ext not in VALID_VIDEO_EXTS:
        raise ToolError(
            f"Unsupported video format '{ext}'. "
            f"Supported: {', '.join(sorted(VALID_VIDEO_EXTS))}"
        )

    if not _check_ffmpeg():
        raise ToolError(
            "ffmpeg is not installed.\n"
            "  Install it in Termux with: pkg install ffmpeg"
        )

    if dry_run:
        return {
            "video_path": video_path,
            "output_path": output_path,
            "dry_run": True,
            "message": f"[DRY RUN] Would extract audio:\n    {video_path}\n  → {output_path}",
            "success": None,
            "errors": [],
        }

    errors: list[str] = []
    success = False

    output_dir = os.path.dirname(output_path) or "."
    try:
        os.makedirs(output_dir, exist_ok=True)
    except Exception as e:
        errors.append(f"Could not create output directory '{output_dir}': {e}")
        return {"video_path": video_path, "output_path": output_path,
                "dry_run": False, "success": False, "errors": errors}

    cmd = [
        "ffmpeg",
        "-i", video_path,
        "-vn",
        "-acodec", "libmp3lame",
        "-q:a", "2",
        "-y",
        output_path,
    ]
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=300,
        )
        if result.returncode == 0:
            success = True
        else:
            stderr = result.stderr.strip()
            errors.append(f"ffmpeg failed (exit {result.returncode}): {stderr[-500:] if stderr else '(no output)'}")
    except subprocess.TimeoutExpired:
        errors.append("ffmpeg timed out after 300 seconds.")
    except Exception as e:
        errors.append(f"ffmpeg error: {e}")

    return {
        "video_path": video_path,
        "output_path": output_path,
        "dry_run": False,
        "success": success,
        "errors": errors,
    }


def compress_images(
    directory: str,
    quality: int = 75,
    dry_run: bool = True,
) -> dict[str, Any]:
    if not os.path.isdir(directory):
        raise ToolError(f"Directory does not exist: {directory}")

    quality = max(1, min(95, quality))

    if not _check_pillow():
        raise ToolError(
            "Pillow is not installed.\n"
            "  Install it with: pip install Pillow"
        )

    from PIL import Image  # type: ignore

    planned: list[str] = []
    compressed: list[str] = []
    errors: list[str] = []

    for entry in os.scandir(directory):
        try:
            if not entry.is_file(follow_symlinks=False):
                continue
        except (OSError, PermissionError):
            continue
        ext = os.path.splitext(entry.name)[1].lower()
        if ext not in COMPRESSIBLE_IMAGE_EXTS:
            continue
        planned.append(entry.path)

    if dry_run:
        return {
            "directory": directory,
            "quality": quality,
            "dry_run": True,
            "planned": planned,
            "compressed": [],
            "errors": [],
        }

    for fpath in planned:
        tmp_path = None
        try:
            img = Image.open(fpath)
            img_format = img.format

            dirpath = os.path.dirname(fpath)
            fd, tmp_path = tempfile.mkstemp(dir=dirpath, suffix=".tmp")
            os.close(fd)

            if img_format == "PNG":
                img.save(tmp_path, format="PNG", optimize=True)
            else:
                img.save(tmp_path, format=img_format or "JPEG",
                         optimize=True, quality=quality)

            os.replace(tmp_path, fpath)
            tmp_path = None
            compressed.append(fpath)
        except Exception as e:
            errors.append(f"Failed to compress '{os.path.basename(fpath)}': {e}")
        finally:
            if tmp_path and os.path.exists(tmp_path):
                try:
                    os.remove(tmp_path)
                except OSError:
                    pass

    return {
        "directory": directory,
        "quality": quality,
        "dry_run": False,
        "planned": planned,
        "compressed": compressed,
        "errors": errors,
    }
