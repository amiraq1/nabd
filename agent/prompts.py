"""
Reference prompts for future non-runtime LLM integration.

The runtime parser remains deterministic and does not depend on this module.
These templates are kept in sync with the current intent list so future
prompt-based experiments do not silently drift from Nabd's real command set.

This module is NOT imported by any MVP runtime path.
"""

from agent.parser import ALL_INTENTS

_SUPPORTED_INTENTS_TEXT = ", ".join(sorted(ALL_INTENTS))

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
{{command}}

Return a JSON object with:
- intent: one of [{_SUPPORTED_INTENTS_TEXT}]
- source_path: optional string
- target_path: optional string
- url: optional string
- app_name: optional string
- query: optional string
- options: dict of extra options
""".format(_SUPPORTED_INTENTS_TEXT=_SUPPORTED_INTENTS_TEXT)
