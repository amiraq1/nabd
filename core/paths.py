import os

from core.config import get_allowed_roots
from core.exceptions import PathNotAllowedError, PathTraversalError, ValidationError

TRAVERSAL_INDICATORS = ["..", "//", "\x00", "%2e", "%2f"]


def resolve_path(raw_path: str) -> str:
    expanded = os.path.expanduser(os.path.expandvars(raw_path))
    resolved = os.path.realpath(os.path.abspath(expanded))
    return resolved


def is_under_allowed_root(resolved_path: str, allowed_roots: list[str]) -> bool:
    for root in allowed_roots:
        resolved_root = os.path.realpath(os.path.abspath(root))
        if resolved_path == resolved_root or resolved_path.startswith(resolved_root + os.sep):
            return True
    return False


def validate_path(raw_path: str) -> str:
    if not raw_path or not raw_path.strip():
        raise ValidationError("Path must not be empty.")

    for indicator in TRAVERSAL_INDICATORS:
        if indicator in raw_path:
            raise PathTraversalError(
                f"Path contains disallowed sequence '{indicator}': {raw_path}"
            )

    resolved = resolve_path(raw_path)
    allowed_roots = get_allowed_roots()

    if not is_under_allowed_root(resolved, allowed_roots):
        raise PathNotAllowedError(
            f"Path '{resolved}' is outside all allowed directories.\n"
            f"Allowed roots: {allowed_roots}"
        )

    return resolved
