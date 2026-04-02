# نبض (Nabd)

A local-first phone operations agent for Android/Termux.

---

## Overview

نبض (Nabd, Arabic for "pulse") is a safe, deterministic CLI assistant for managing files on your Android device via Termux. It accepts natural language commands in **Arabic or English**, translates them into structured operations, previews changes before applying them, and requires your confirmation for any action that modifies files.

---

## Product Boundaries

نبض is:
- A controlled, local command assistant for file and storage operations
- Designed for safety-first, deterministic execution
- Runnable entirely offline on your Android device

نبض is NOT:
- A general chatbot or AI agent
- An unrestricted shell executor
- A cloud service or web application
- An autonomous deletion or cleanup bot

---

## Safety Model

- All file access is restricted to paths listed in `config/allowed_paths.json`
- Path traversal attacks (`..`, `//`, null bytes) are detected and rejected
- All modifying operations require explicit yes/no confirmation
- High-risk operations (compress, rename) are flagged clearly
- No arbitrary shell commands are ever executed
- No destructive action is taken without your approval
- All operations are logged to a local SQLite database

---

## Architecture

```
nabd/
  main.py               — Interactive CLI entry point
  requirements.txt      — Python dependencies
  README.md             — This file
  agent/
    models.py           — Typed data models (ParsedIntent, ExecutionPlan, etc.)
    parser.py           — Rule-based Arabic/English command parser
    planner.py          — Maps intents to deterministic execution plans
    safety.py           — Central safety enforcement layer
    executor.py         — Executes only whitelisted tool functions
    reporter.py         — Generates readable result summaries
    prompts.py          — Placeholder for future LLM integration
  tools/
    storage.py          — Storage reports, large file listing
    files.py            — Organize, rename, move files
    media.py            — Video-to-MP3 conversion, image compression
    backup.py           — Safe folder backup
    duplicates.py       — Duplicate file detection via SHA-256 hashing
    utils.py            — Shared helpers (size formatting, file scanning, hashing)
  core/
    config.py           — Loads JSON configuration
    paths.py            — Safe path resolution and validation
    logging_db.py       — SQLite operation history logging
    exceptions.py       — Custom exception hierarchy
  config/
    allowed_paths.json  — List of allowed root directories
    settings.json       — Application settings
  data/                 — SQLite log database (auto-created)
  tests/
    test_parser.py      — Parser intent detection tests
    test_safety.py      — Path safety validation tests
    test_tools.py       — Tool function tests
```

---

## Supported Features (MVP)

| Intent | English Example | Arabic Example |
|---|---|---|
| Storage report | `storage report /sdcard/Download` | `تقرير التخزين /sdcard/Download` |
| List large files | `list large files /sdcard` | `اعرض أكبر الملفات /sdcard` |
| Organize folder | `organize /sdcard/Download` | `رتّب مجلد /sdcard/Download` |
| Find duplicates | `find duplicates /sdcard/Download` | `ابحث عن الملفات المكررة /sdcard/Download` |
| Backup folder | `back up /sdcard/Documents to /sdcard/Backup` | `انسخ /sdcard/Documents احتياطيًا إلى /sdcard/Backup` |
| Convert to MP3 | `convert /sdcard/Movies/film.mp4 to mp3` | `حوّل /sdcard/Movies/film.mp4 إلى mp3` |
| Compress images | `compress images /sdcard/Pictures` | `اضغط صور /sdcard/Pictures` |
| Safe rename | `rename files /sdcard/Download prefix old_` | `إعادة تسمية /sdcard/Download` |
| Safe move | `move /sdcard/Download/file.txt to /sdcard/Documents` | `انقل /sdcard/Download/file.txt إلى /sdcard/Documents` |

---

## Termux Installation

### 1. Update Termux

```bash
pkg update && pkg upgrade
```

### 2. Install Python

```bash
pkg install python
```

### 3. Install ffmpeg (required for video conversion)

```bash
pkg install ffmpeg
```

### 4. Grant storage permission

```bash
termux-setup-storage
```

This creates symlinks under `~/storage/` pointing to `/sdcard/`.

### 5. Clone the repository

```bash
git clone https://github.com/youruser/nabd.git
cd nabd
```

### 6. Install Python dependencies

```bash
pip install -r requirements.txt
```

Pillow is required for image compression. All other features use only the Python standard library.

---

## Running نبض

```bash
python main.py
```

You will see an interactive prompt:

```
نبض> 
```

Type your command and press Enter. Type `exit` or `خروج` to quit.

---

## Configuration

### `config/allowed_paths.json`

Defines the directories the agent is permitted to access. Edit this file to add or remove allowed roots.

```json
{
  "allowed_roots": [
    "/sdcard/Download",
    "/sdcard/Documents",
    "/sdcard/Music",
    "/sdcard/Movies",
    "/sdcard/Pictures"
  ]
}
```

Any path outside these roots will be rejected automatically.

### `config/settings.json`

Application behavior settings:

```json
{
  "max_large_files": 20,
  "large_file_threshold_mb": 10,
  "image_compress_quality": 75,
  "require_confirmation_for_modifying": true
}
```

---

## Running Tests

```bash
cd nabd
python -m pytest tests/ -v
```

Tests cover:
- Intent detection in Arabic and English
- Path traversal prevention
- Allowed path enforcement
- Dry-run and actual file operations
- Duplicate detection
- Storage report accuracy

---

## Future Roadmap

- Optional local LLM integration for more flexible command parsing
- Scheduled operations (cron-style)
- Undo / rollback for recent modifying operations
- Rich terminal UI (curses or Textual)
- Recursive duplicate deletion with keepers selection
- WhatsApp media cleanup tool
- Smart photo deduplication (perceptual hashing)
- Export operation history to CSV

---

## License

MIT
