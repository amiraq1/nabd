"""
LocalBackend — a deterministic, keyword-matching backend.

No neural network, no API, no internet required.
Always available. Confidence scores reflect keyword-overlap strength,
NOT neural inference probability. This is stated honestly in all outputs.
"""
from __future__ import annotations

from llm.backend import LLMBackend
from llm.schemas import BackendStatus, CommandSuggestion, Clarification, IntentSuggestion, ResultExplanation


# ── Keyword → intent mapping ──────────────────────────────────────────────────

INTENT_KEYWORDS: dict[str, list[str]] = {
    "doctor": [
        "check", "setup", "health", "ready", "environment",
        "diagnose", "test", "tools", "working", "ok", "okay", "verify",
    ],
    "storage_report": [
        "storage", "space", "disk", "size", "how much",
        "capacity", "free", "used", "usage", "full",
    ],
    "list_large_files": [
        "large files", "big files", "largest", "biggest", "heavy files", "top files",
    ],
    "show_files": [
        "files", "list files", "show files", "browse", "contents",
        "what's in", "what is in", "inside",
    ],
    "show_folders": [
        "folders", "directories", "subdirectories", "subfolders",
        "list folders", "show folders",
    ],
    "list_media": [
        "media", "photos", "images", "videos", "audio",
        "music", "pictures", "clips",
    ],
    "organize_folder_by_type": [
        "organize", "sort", "arrange", "tidy", "categorize",
        "group", "clean up",
    ],
    "find_duplicates": [
        "duplicate", "duplicates", "same files", "identical",
        "copies", "repeated", "double",
    ],
    "backup_folder": [
        "backup", "back up", "copy", "save", "archive", "snapshot",
    ],
    "convert_video_to_mp3": [
        "convert", "mp3", "audio from video", "extract audio", "video to audio",
    ],
    "compress_images": [
        "compress", "resize images", "shrink images",
        "reduce image size", "optimize images",
    ],
    "safe_rename_files": [
        "rename", "prefix", "suffix", "add prefix", "add suffix",
    ],
    "safe_move_files": [
        "move", "transfer file", "relocate",
    ],
    "open_url": [
        "open url", "open website", "open link", "open http", "visit",
    ],
    "open_file": [
        "open file", "view file", "read file",
    ],
    "open_app": [
        "open app", "launch", "start app", "open chrome",
        "open camera", "open settings",
    ],
    "phone_status_battery": [
        "battery", "charge", "power level", "battery status",
    ],
    "phone_status_network": [
        "network", "wifi", "internet", "connection", "ip address",
    ],
    "browser_search": [
        "search", "google", "look up", "web search", "online search",
    ],
    "browser_extract_text": [
        "extract text", "read page", "scrape", "text from", "fetch text", "get text",
    ],
    "browser_list_links": [
        "list links", "links on", "find links", "get links",
    ],
    "browser_page_title": [
        "page title", "title of", "get title", "show title",
    ],
}

# ── Example command strings shown to the user ─────────────────────────────────

INTENT_EXAMPLES: dict[str, str] = {
    "doctor":                   "doctor",
    "storage_report":           "storage report /sdcard/Download",
    "list_large_files":         "list large files /sdcard/Download",
    "show_files":               "show files in /sdcard/Download",
    "show_folders":             "show folders in /sdcard/Download",
    "list_media":               "list media in /sdcard/Download",
    "organize_folder_by_type":  "organize /sdcard/Download",
    "find_duplicates":          "find duplicates /sdcard/Download",
    "backup_folder":            "back up /sdcard/Documents to /sdcard/Backup",
    "convert_video_to_mp3":     "convert /sdcard/Movies/film.mp4 to mp3",
    "compress_images":          "compress images /sdcard/Pictures",
    "safe_rename_files":        "rename files /sdcard/Download prefix old_",
    "safe_move_files":          "move /sdcard/Download/file.txt to /sdcard/Documents",
    "open_url":                 "open https://example.com",
    "open_file":                "open file /sdcard/Download/report.pdf",
    "open_app":                 "open chrome",
    "phone_status_battery":     "show battery status",
    "phone_status_network":     "show network status",
    "browser_search":           "search for local llm tools",
    "browser_extract_text":     "extract text from https://example.com",
    "browser_list_links":       "list links from https://example.com",
    "browser_page_title":       "show page title from https://example.com",
}

INTENT_RATIONALE: dict[str, str] = {
    "doctor":                   "checks Python, ffmpeg, Pillow, allowed paths, and storage access",
    "storage_report":           "shows total size, file count, and breakdown by category",
    "list_large_files":         "lists the biggest files sorted by size",
    "show_files":               "lists every file and folder in a directory",
    "show_folders":             "lists only subfolders with item counts",
    "list_media":               "lists images, videos, and audio files grouped by type",
    "organize_folder_by_type":  "moves files into images/, videos/, documents/ subfolders (preview first)",
    "find_duplicates":          "finds identical files via SHA-256 hash — no files deleted",
    "backup_folder":            "copies a folder to a timestamped backup directory",
    "convert_video_to_mp3":     "extracts audio from a video file (requires ffmpeg)",
    "compress_images":          "re-saves images at lower quality to save space (requires Pillow)",
    "safe_rename_files":        "adds a prefix or suffix to every filename in a folder",
    "safe_move_files":          "moves a file or folder to a new location",
    "open_url":                 "opens a URL in the default browser",
    "open_file":                "opens a local file in the appropriate app",
    "open_app":                 "launches a supported Android app",
    "phone_status_battery":     "checks battery level, status, health, and temperature",
    "phone_status_network":     "checks wifi name, IP address, and signal strength",
    "browser_search":           "opens a web search in the default browser",
    "browser_extract_text":     "fetches a page and returns readable text (no account needed)",
    "browser_list_links":       "fetches a page and lists all links found on it",
    "browser_page_title":       "fetches only the page title — fast and minimal",
}

# ── Clarification question templates ──────────────────────────────────────────

_CLARIFY_RULES: list[tuple[list[str], str]] = [
    (
        ["media", "photo", "image", "picture", "video", "music", "audio", "clip"],
        "Do you want to list media in one folder only, or scan all subfolders too (recursively)?",
    ),
    (
        ["backup", "back up", "copy", "archive"],
        "Which folder do you want to back up, and where should the backup be stored?",
    ),
    (
        ["organize", "sort", "arrange", "tidy", "clean up"],
        "Which folder do you want to organize into subfolders by type?",
    ),
    (
        ["open"],
        "Do you want to open an app (e.g. open chrome), a local file, or a URL?",
    ),
    (
        ["rename"],
        "Do you want to add a prefix or a suffix to the filenames?",
    ),
    (
        ["move", "transfer"],
        "Which file do you want to move, and which folder should it go into?",
    ),
    (
        ["search", "find", "look"],
        "Are you looking for duplicate files, large files, or do you want a web search?",
    ),
]

# ── Explain-result intent-phrase map ──────────────────────────────────────────

_EXPLAIN_MAP: list[tuple[list[str], str, str | None, str | None]] = [
    (
        ["doctor"],
        "This command checked your Nabd environment (Python, ffmpeg, Pillow, storage access).",
        None,
        "If any checks failed, follow the instructions shown to fix them.",
    ),
    (
        ["storage report", "storage"],
        "This command reported storage usage and the breakdown by file category.",
        None,
        "Run 'list large files <path>' to see the biggest files.",
    ),
    (
        ["list large files", "large files"],
        "This command listed the biggest files in the directory, sorted by size.",
        None,
        None,
    ),
    (
        ["show files", "browse"],
        "This command listed the files and folders in the specified directory.",
        None,
        None,
    ),
    (
        ["show folders"],
        "This command listed only the subfolders with item counts.",
        None,
        None,
    ),
    (
        ["list media", "media"],
        "This command listed media files (images, videos, audio) in the directory.",
        None,
        "Add 'recursively' to scan all subfolders too.",
    ),
    (
        ["organize"],
        "This command previewed moving files into category subfolders.",
        "No files were moved — this was a preview only.",
        "If the plan looks right, run the command again and confirm with 'y'.",
    ),
    (
        ["back up", "backup"],
        "This command copied your folder to a timestamped backup directory.",
        None,
        "Run 'show files in <backup path>' to verify the backup.",
    ),
    (
        ["find duplicates", "duplicate"],
        "This command scanned for identical files using SHA-256 hashing.",
        "Nabd does not delete files — you control what to remove.",
        "Review the list and manually delete duplicates you no longer need.",
    ),
    (
        ["battery"],
        "This command showed your phone's current battery level and health.",
        None,
        None,
    ),
    (
        ["network"],
        "This command showed your wifi connection name, IP address, and signal.",
        None,
        None,
    ),
    (
        ["compress images", "compress"],
        "This command re-saved images at a lower quality to reduce file size.",
        "Compression overwrites originals — ensure you have a backup first.",
        None,
    ),
    (
        ["rename"],
        "This command added a prefix or suffix to filenames in the folder.",
        None,
        None,
    ),
    (
        ["move"],
        "This command moved a file or folder to a new location.",
        None,
        None,
    ),
    (
        ["extract text"],
        "This command fetched a web page and extracted readable text.",
        None,
        None,
    ),
    (
        ["list links"],
        "This command fetched a web page and listed all links found on it.",
        None,
        None,
    ),
    (
        ["page title"],
        "This command fetched the page title from the URL.",
        None,
        None,
    ),
    (
        ["search", "google"],
        "This command opened a web search in your default browser.",
        None,
        None,
    ),
    (
        ["open"],
        "This command launched an app, file, or URL on your phone.",
        None,
        None,
    ),
]


# ── LocalBackend implementation ───────────────────────────────────────────────

class LocalBackend(LLMBackend):
    """
    Deterministic keyword-matching backend.
    Always available. No ML model, no network, no API keys.
    """

    def is_available(self) -> bool:
        return True

    def get_status(self) -> BackendStatus:
        return BackendStatus(
            available=True,
            backend_name="local",
            transport=None,
            healthy=True,
            detail="Deterministic keyword matching — always available, no server required.",
        )

    def suggest_command(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> CommandSuggestion:
        text = user_text.lower()
        scored: list[tuple[float, str]] = []

        for intent in available_intents:
            keywords = INTENT_KEYWORDS.get(intent, [])
            if not keywords:
                continue
            hits = sum(1 for kw in keywords if kw in text)
            if hits > 0:
                confidence = min(1.0, hits / max(len(keywords), 4))
                scored.append((confidence, intent))

        if not scored:
            return CommandSuggestion(
                suggested_command="doctor",
                rationale=(
                    "No specific match found. 'doctor' checks your environment "
                    "and is always a safe starting point."
                ),
                confidence=0.1,
            )

        scored.sort(reverse=True)
        best_conf, best_intent = scored[0]
        example = INTENT_EXAMPLES.get(best_intent, best_intent)
        rationale = INTENT_RATIONALE.get(best_intent, "matches your description")

        return CommandSuggestion(
            suggested_command=example,
            rationale=rationale,
            confidence=round(best_conf, 2),
        )

    def explain_result(
        self,
        last_command: str,
        last_result: str,
    ) -> ResultExplanation:
        if not last_command:
            return ResultExplanation(
                summary="No previous command found in this session.",
                safety_note=None,
                suggested_next_step="Run a command first, then ask 'explain last result'.",
            )

        cmd_lower = last_command.lower()

        for phrases, summary, safety_note, next_step in _EXPLAIN_MAP:
            if any(phrase in cmd_lower for phrase in phrases):
                return ResultExplanation(
                    summary=summary,
                    safety_note=safety_note,
                    suggested_next_step=next_step,
                )

        return ResultExplanation(
            summary=f"You ran: '{last_command}'.",
            safety_note=None,
            suggested_next_step=None,
        )

    def clarify_request(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> Clarification:
        text = user_text.lower()

        # Collect candidate intents
        candidates: list[str] = []
        for intent in available_intents:
            keywords = INTENT_KEYWORDS.get(intent, [])
            if any(kw in text for kw in keywords):
                candidates.append(intent)

        # Find the first matching clarification rule
        for kw_list, question in _CLARIFY_RULES:
            if any(kw in text for kw in kw_list):
                return Clarification(
                    clarification_needed=True,
                    clarification_question=question,
                    candidate_intents=candidates[:3],
                )

        return Clarification(
            clarification_needed=bool(candidates),
            clarification_question=(
                "Could you be more specific? For example: which folder, what action, "
                "or which app do you have in mind?"
                if candidates else
                "I'm not sure what you'd like to do. Type 'help' to see all Nabd commands."
            ),
            candidate_intents=candidates[:3],
        )

    def suggest_intent(
        self,
        user_text: str,
        allowed_intents: list[str],
    ) -> IntentSuggestion:
        suggestion = self.suggest_command(user_text, allowed_intents)

        # Map the example command back to its intent
        for intent in allowed_intents:
            example = INTENT_EXAMPLES.get(intent, "")
            if example and example.split()[0] in suggestion.suggested_command.split()[0]:
                return IntentSuggestion(
                    intent=intent,
                    confidence=suggestion.confidence,
                    explanation=suggestion.rationale,
                )

        # Fallback: check keyword overlap against intent names directly
        text = user_text.lower()
        for intent in allowed_intents:
            keywords = INTENT_KEYWORDS.get(intent, [])
            if any(kw in text for kw in keywords):
                return IntentSuggestion(
                    intent=intent,
                    confidence=suggestion.confidence,
                    explanation=suggestion.rationale,
                )

        return IntentSuggestion(
            intent=None,
            confidence=0.0,
            explanation="Could not confidently match your request to a known Nabd command.",
        )
