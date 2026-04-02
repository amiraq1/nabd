import re
from typing import Optional

from agent.models import ParsedIntent, RiskLevel
from core.exceptions import UnknownIntentError


INTENT_PATTERNS: list[tuple[str, list[str]]] = [
    # ── Diagnostic ────────────────────────────────────────────────────────
    ("doctor", [
        r"\bdoctor\b",
        r"check\s+(?:setup|env(?:ironment)?|install(?:ation)?|dependencies)",
        r"diagnos(?:e|is|tic)",
        r"health\s+check",
        r"verify\s+(?:setup|install)",
        r"is\s+(?:ffmpeg|pillow|python)\s+installed",
    ]),
    # ── Storage ───────────────────────────────────────────────────────────
    ("storage_report", [
        r"storage\s+report", r"disk\s+usage", r"how\s+much\s+space",
        r"storage\s+status", r"check\s+storage", r"show\s+storage",
    ]),
    ("list_large_files", [
        r"large\s+files?", r"biggest?\s+files?", r"largest?\s+files?",
        r"top\s+\d+\s*files?", r"show.*files.*size", r"what.*taking.*space",
        r"files?\s+by\s+size",
    ]),
    # ── Compress (before list_media to avoid "compress images in ..." ambiguity) ──
    ("compress_images", [
        r"compress\s+images?", r"resize\s+images?", r"shrink\s+images?",
        r"optimize\s+images?", r"reduce\s+image\s+size",
    ]),
    # ── Browse ────────────────────────────────────────────────────────────
    ("show_files", [
        r"show\s+files?\s+in\b",
        r"list\s+files?\s+in\b",
        r"what\s+(?:files?\s+)?(?:is|are)\s+in\b",
        r"contents?\s+of\b",
        r"\bls\b(?:\s|$)",
        r"browse\b",
    ]),
    ("list_media", [
        r"list\s+media\b",
        r"show\s+media\b",
        r"media\s+files?\b",
        r"(?:show|find|list)\s+(?:all\s+)?(?:photos?|images?|videos?|clips?|audio|music|songs?)\b",
        r"(?:photos?|images?|videos?|audio|music)\s+in\b",
    ]),
    # ── File management ───────────────────────────────────────────────────
    ("organize_folder_by_type", [
        r"organiz(?:e|ing)\b", r"\bsort\s+files?\b", r"\barrange\s+files?\b",
        r"\btidy\s+(?:up\s+)?files?\b", r"clean\s+up\s+(?:the\s+)?folder",
        r"group\s+files?\s+by\s+type",
    ]),
    ("find_duplicates", [
        r"duplicates?", r"duplicate\s+files?", r"repeated\s+files?",
        r"find.*same\s+files?", r"identical\s+files?",
        r"redundant\s+files?",
    ]),
    ("backup_folder", [
        r"back\s*up\b", r"\bbackup\b", r"copy\s+folder", r"make.*copy",
        r"create.*backup", r"mirror\s+folder",
    ]),
    ("convert_video_to_mp3", [
        r"convert.*(?:video|mp4|mkv|avi|mov)\b.*(?:mp3|audio)\b",
        r"extract.*audio\b", r"(?:mp3|audio)\s+from\b",
        r"to\s+mp3\b", r"as\s+mp3\b",
        r"rip.*audio",
    ]),
    ("safe_rename_files", [
        r"rename\s+files?", r"batch\s+rename", r"bulk\s+rename",
    ]),
    ("safe_move_files", [
        r"move\s+(?:the\s+)?(?:file|folder|directory)\b",
        r"move\s+/", r"transfer\s+files?\b",
        r"relocate\s+files?\b",
    ]),
]

MODIFYING_INTENTS = {
    "organize_folder_by_type",
    "backup_folder",
    "convert_video_to_mp3",
    "compress_images",
    "safe_rename_files",
    "safe_move_files",
}

HIGH_RISK_INTENTS = {
    "compress_images",
    "safe_rename_files",
}

_PATH_RE = re.compile(r'(?:^|[\s"\'])(/[^\s"\',:;]+)')
_QUOTED_RE = re.compile(r'["\']([^"\']+)["\']')
_TO_SEP_RE = re.compile(
    r'\bto\b\s+["\']?(/[^\s"\',:;]+)["\']?',
    re.IGNORECASE
)


def detect_intent(command: str) -> str:
    text = command.lower().strip()
    for intent_name, patterns in INTENT_PATTERNS:
        for pattern in patterns:
            if re.search(pattern, text):
                return intent_name
    raise UnknownIntentError(
        f"Could not understand: '{command}'\n"
        "  Type 'help' to see all supported commands."
    )


def _extract_all_paths(command: str) -> list[str]:
    paths: list[str] = []
    seen: set[str] = set()

    for m in _QUOTED_RE.finditer(command):
        candidate = m.group(1).strip()
        if candidate.startswith("/") and candidate not in seen:
            paths.append(candidate)
            seen.add(candidate)

    for m in _PATH_RE.finditer(command):
        candidate = m.group(1).strip().rstrip(".,;)")
        if candidate not in seen:
            paths.append(candidate)
            seen.add(candidate)

    return paths


def _extract_source_target(command: str) -> tuple[Optional[str], Optional[str]]:
    to_match = _TO_SEP_RE.search(command)
    if to_match:
        target_path = to_match.group(1).strip().rstrip(".,;)")
        all_paths = _extract_all_paths(command)
        source_path = None
        for p in all_paths:
            if p != target_path:
                source_path = p
                break
        if source_path is None and all_paths:
            source_path = all_paths[0]
        return source_path, target_path

    all_paths = _extract_all_paths(command)
    if len(all_paths) >= 2:
        return all_paths[0], all_paths[1]
    elif len(all_paths) == 1:
        return all_paths[0], None
    return None, None


def extract_options(command: str, intent: str) -> dict:
    options: dict = {}

    top_n_match = re.search(r"\btop\s+(\d+)", command, re.IGNORECASE)
    if top_n_match:
        options["top_n"] = int(top_n_match.group(1))

    sort_match = re.search(r"\bsort(?:ed)?\s+by\s+(name|size|modified|date)\b", command, re.IGNORECASE)
    if sort_match:
        val = sort_match.group(1).lower()
        options["sort_by"] = "modified" if val in ("modified", "date") else val

    if intent == "show_files":
        if not top_n_match:
            limit_match = re.search(r"\b(?:limit|show|first|last)\s+(\d+)\b", command, re.IGNORECASE)
            if limit_match:
                options["limit"] = int(limit_match.group(1))
        recursive_match = re.search(r"\b(?:recursive(?:ly)?|all\s+subfolders?|deep)\b", command, re.IGNORECASE)
        if recursive_match:
            options["recursive"] = True

    if intent == "list_media":
        recursive_match = re.search(r"\b(?:recursive(?:ly)?|all\s+subfolders?|deep)\b", command, re.IGNORECASE)
        if recursive_match:
            options["recursive"] = True

    if intent == "safe_rename_files":
        prefix_match = re.search(r"prefix\s+[\"']?(\S+)[\"']?", command, re.IGNORECASE)
        suffix_match = re.search(r"suffix\s+[\"']?(\S+)[\"']?", command, re.IGNORECASE)
        if prefix_match:
            options["prefix"] = prefix_match.group(1)
        if suffix_match:
            options["suffix"] = suffix_match.group(1)

    if intent == "compress_images":
        quality_match = re.search(r"quality\s+(\d+)", command, re.IGNORECASE)
        if quality_match:
            q = int(quality_match.group(1))
            options["quality"] = max(1, min(95, q))

    return options


def parse_command(command: str) -> ParsedIntent:
    command = command.strip()
    intent = detect_intent(command)
    source_path, target_path = _extract_source_target(command)
    options = extract_options(command, intent)

    is_modifying = intent in MODIFYING_INTENTS
    is_high_risk = intent in HIGH_RISK_INTENTS

    risk_level = RiskLevel.LOW
    if is_modifying:
        risk_level = RiskLevel.HIGH if is_high_risk else RiskLevel.MEDIUM

    return ParsedIntent(
        intent=intent,
        source_path=source_path,
        target_path=target_path,
        options=options,
        risk_level=risk_level,
        requires_confirmation=is_modifying,
        raw_command=command,
    )
