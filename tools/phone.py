"""
tools/phone.py — Controlled wrappers for phone/Android operations.

All subprocess calls use fixed command lists — user input is NEVER
interpolated into shell strings.  Only functions in SUPPORTED_APPS
may be launched via open_app().
"""

import json
import subprocess
from typing import Any

from core.exceptions import ToolError


# ── Supported app registry ────────────────────────────────────────────────────
# Maps friendly user-facing names to safe, fixed am-start commands.
# No arbitrary package names accepted from users.

SUPPORTED_APPS: dict[str, dict[str, Any]] = {
    "chrome": {
        "description": "Google Chrome browser",
        "command": [
            "am", "start",
            "-n", "com.android.chrome/com.google.android.apps.chrome.Main",
        ],
    },
    "settings": {
        "description": "Android Settings",
        "command": ["am", "start", "-a", "android.settings.SETTINGS"],
    },
    "files": {
        "description": "Files by Google (file manager)",
        "command": [
            "am", "start",
            "-n", "com.google.android.apps.nbu.files/.home.HomeActivity",
        ],
    },
    "camera": {
        "description": "Camera app",
        "command": [
            "am", "start",
            "-a", "android.media.action.IMAGE_CAPTURE",
        ],
    },
    "gallery": {
        "description": "Photos / Gallery app",
        "command": [
            "am", "start",
            "-a", "android.intent.action.VIEW",
            "-t", "image/*",
        ],
    },
    "calculator": {
        "description": "Calculator app",
        "command": ["am", "start", "-a", "android.intent.action.MAIN",
                    "-n", "com.android.calculator2/.Calculator"],
    },
}


def _run(cmd: list[str], timeout: int = 10) -> tuple[int, str, str]:
    """
    Execute a fixed, whitelisted command list (never shell=True).
    Returns (returncode, stdout, stderr).
    """
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=timeout,
            shell=False,
        )
        return result.returncode, result.stdout.strip(), result.stderr.strip()
    except FileNotFoundError:
        return 127, "", f"Command not found: {cmd[0]!r}. Is termux-api installed and in PATH?"
    except subprocess.TimeoutExpired:
        return 1, "", f"Command timed out after {timeout}s."
    except PermissionError as e:
        return 1, "", f"Permission denied running {cmd[0]!r}: {e}"
    except Exception as e:
        return 1, "", f"Unexpected error running {cmd[0]!r}: {e}"


# ── Public tool functions ─────────────────────────────────────────────────────

def open_app(app_name: str) -> dict[str, Any]:
    """
    Launch a supported Android app by friendly name.
    Rejects any app name not in SUPPORTED_APPS.
    """
    key = app_name.lower().strip()
    if key not in SUPPORTED_APPS:
        return {
            "success": False,
            "app_name": app_name,
            "error": (
                f"App '{app_name}' is not in the supported list.\n"
                f"  Supported apps: {', '.join(sorted(SUPPORTED_APPS))}"
            ),
            "supported_apps": sorted(SUPPORTED_APPS.keys()),
        }

    app = SUPPORTED_APPS[key]
    rc, stdout, stderr = _run(app["command"])
    return {
        "success": rc == 0,
        "app_name": key,
        "description": app["description"],
        "error": stderr if rc != 0 else None,
    }


def open_file(path: str) -> dict[str, Any]:
    """
    Open a local file in the appropriate Android app via termux-open.
    Path safety is enforced at the safety layer before this is called.
    """
    rc, stdout, stderr = _run(["termux-open", path])
    return {
        "success": rc == 0,
        "path": path,
        "error": stderr if rc != 0 else None,
    }


def open_url(url: str) -> dict[str, Any]:
    """
    Open a URL in the default browser via termux-open-url.
    URL safety (scheme validation, no javascript:) is enforced upstream.
    """
    rc, stdout, stderr = _run(["termux-open-url", url])
    return {
        "success": rc == 0,
        "url": url,
        "error": stderr if rc != 0 else None,
    }


def get_battery_status() -> dict[str, Any]:
    """
    Return battery status from termux-battery-status (JSON).
    Returns a dict with 'success' + battery fields on success,
    or 'success': False + 'error' on failure.
    """
    rc, stdout, stderr = _run(["termux-battery-status"])
    if rc != 0 or not stdout:
        return {
            "success": False,
            "error": (
                stderr or
                "termux-battery-status returned no output. "
                "Install termux-api: pkg install termux-api"
            ),
        }
    try:
        data = json.loads(stdout)
        return {"success": True, **data}
    except json.JSONDecodeError:
        return {
            "success": False,
            "error": f"Could not parse termux-battery-status output: {stdout!r}",
        }


def get_network_status() -> dict[str, Any]:
    """
    Return wifi / network info from termux-wifi-connectioninfo (JSON).
    """
    rc, stdout, stderr = _run(["termux-wifi-connectioninfo"])
    if rc != 0 or not stdout:
        return {
            "success": False,
            "error": (
                stderr or
                "termux-wifi-connectioninfo returned no output. "
                "Install termux-api: pkg install termux-api"
            ),
        }
    try:
        data = json.loads(stdout)
        return {"success": True, **data}
    except json.JSONDecodeError:
        return {
            "success": False,
            "error": f"Could not parse termux-wifi-connectioninfo output: {stdout!r}",
        }
