"""
Placeholder for future LLM integration.

This module is intentionally minimal. The MVP uses deterministic rule-based
parsing and does not require any LLM. When LLM integration is added, this
module will provide prompt templates and model interaction helpers.

This module is NOT imported by any MVP runtime path.
"""

SYSTEM_PROMPT_TEMPLATE = """
You are نبض (Nabd), a local phone operations assistant.
You help users organize files, analyze storage, and perform safe file operations
on their Android device. You operate only on allowed directories and never
execute arbitrary shell commands.

Always respond in the user's language (Arabic or English).
Always confirm before making any changes.
"""

INTENT_EXTRACTION_TEMPLATE = """
Extract the user's intent from the following command:
{command}

Return a JSON object with:
- intent: one of [storage_report, list_large_files, organize_folder_by_type,
  convert_video_to_mp3, compress_images, backup_folder, find_duplicates,
  safe_rename_files, safe_move_files]
- source_path: optional string
- target_path: optional string
- options: dict of extra options
"""
