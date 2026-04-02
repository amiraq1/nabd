import os
import shutil
import sys
from typing import Any


def run_doctor() -> dict[str, Any]:
    """
    Check the Nabd environment and report the status of each dependency.
    Returns a structured dict with per-check results and an overall status.
    """
    checks: list[dict[str, str]] = []

    # ── 1. Python version ───────────────────────────────────────────────────
    major, minor = sys.version_info.major, sys.version_info.minor
    py_ok = (major, minor) >= (3, 10)
    checks.append({
        "name": "Python version",
        "status": "ok" if py_ok else "warn",
        "detail": (
            f"Python {major}.{minor}.{sys.version_info.micro}"
            + (" (supported)" if py_ok else " (recommend 3.10+)")
        ),
    })

    # ── 2. ffmpeg (video → MP3 conversion) ──────────────────────────────────
    ffmpeg_path = shutil.which("ffmpeg")
    checks.append({
        "name": "ffmpeg",
        "status": "ok" if ffmpeg_path else "missing",
        "detail": (
            ffmpeg_path
            if ffmpeg_path
            else "Not found — run:  pkg install ffmpeg"
        ),
    })

    # ── 3. Pillow (image compression) ───────────────────────────────────────
    try:
        import PIL
        checks.append({
            "name": "Pillow (image compression)",
            "status": "ok",
            "detail": f"version {PIL.__version__}",
        })
    except ImportError:
        checks.append({
            "name": "Pillow (image compression)",
            "status": "missing",
            "detail": "Not installed — run:  pip install Pillow",
        })

    # ── 4. Allowed paths accessible ─────────────────────────────────────────
    try:
        from core.config import get_allowed_roots
        allowed_roots = get_allowed_roots()
    except Exception:
        allowed_roots = []

    accessible = [r for r in allowed_roots if os.path.isdir(r)]
    inaccessible = [r for r in allowed_roots if not os.path.isdir(r)]

    if not allowed_roots:
        ap_status = "error"
        ap_detail = "No allowed paths configured — check config/allowed_paths.json"
    elif accessible:
        ap_status = "ok" if not inaccessible else "warn"
        parts = [f"{len(accessible)}/{len(allowed_roots)} paths reachable"]
        if inaccessible:
            parts.append(f"missing: {', '.join(inaccessible)}")
        ap_detail = " | ".join(parts)
    else:
        ap_status = "warn"
        ap_detail = (
            "No allowed paths are accessible on this device.\n"
            "  Run 'termux-setup-storage' to grant storage permission."
        )

    checks.append({
        "name": "Allowed paths",
        "status": ap_status,
        "detail": ap_detail,
    })

    # ── 5. Data directory writable (history log) ────────────────────────────
    script_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    data_dir = os.path.join(script_dir, "data")
    data_writable = False
    try:
        os.makedirs(data_dir, exist_ok=True)
        test_path = os.path.join(data_dir, ".write_test")
        with open(test_path, "w") as fh:
            fh.write("nabd_doctor_test")
        os.remove(test_path)
        data_writable = True
    except (OSError, PermissionError):
        pass

    checks.append({
        "name": "History log directory",
        "status": "ok" if data_writable else "error",
        "detail": (
            f"{data_dir} (writable)"
            if data_writable
            else f"{data_dir} — NOT writable. Check directory permissions."
        ),
    })

    # ── 6. HTTPS / CA certificate check (browser extract & list-links) ──────
    try:
        from tools.browser import check_browser_tls
        tls_result = check_browser_tls()
        checks.append({
            "name": "HTTPS / CA certificates",
            "status": tls_result["status"],
            "detail": tls_result["detail"],
        })
    except Exception as e:
        checks.append({
            "name": "HTTPS / CA certificates",
            "status": "warn",
            "detail": f"TLS check skipped: {e}",
        })

    ok_count = sum(1 for c in checks if c["status"] == "ok")
    warn_count = sum(1 for c in checks if c["status"] == "warn")
    error_count = sum(1 for c in checks if c["status"] in ("missing", "error"))
    overall = "ok" if error_count == 0 and warn_count == 0 else (
        "warn" if error_count == 0 else "error"
    )

    return {
        "checks": checks,
        "ok_count": ok_count,
        "warn_count": warn_count,
        "error_count": error_count,
        "overall": overall,
    }
