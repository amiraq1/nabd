# Nabd

A local-first, safety-first phone operations agent for Android/Termux.

Nabd accepts natural-language commands in English, turns them into structured intents, validates every path against an allowlist, shows a preview before making any change, and asks for confirmation before anything modifying is applied.

**v0.4** — 397 tests passing.

---

## Design principles

- **Local-only** — no cloud, no network, no external AI APIs required for core functionality.
- **Safety first** — every path is checked against `config/allowed_paths.json`. Path traversal (`..`) is blocked at the parser. URL schemes are validated (only `https://` and `http://` permitted).
- **Preview before apply** — modifying operations run a dry-run pass first. You see exactly what will change.
- **Explicit confirmation** — you type `y` before any file is moved, renamed, or overwritten, or before a URL/file is opened.
- **Whitelisted execution** — only explicitly declared tool functions can be called. No arbitrary shell execution. No arbitrary browser automation.
- **English-only** — clean input; no multi-language ambiguity.

---

## Requirements

| Dependency | Used for | Install |
|---|---|---|
| Python 3.10+ | core runtime | built-in on Termux |
| ffmpeg | video → MP3 conversion | `pkg install ffmpeg` |
| Pillow | image compression | `pip install Pillow` |
| termux-api | phone/browser commands | `pkg install termux-api` |

Run `doctor` inside Nabd to check all dependencies at once.

---

## Installation (Termux)

```bash
pkg update && pkg upgrade
pkg install python ffmpeg
termux-setup-storage       # grant storage permission
git clone https://github.com/amiraq1/nabd
cd nabd
pip install -r requirements.txt
python main.py
```

For phone and browser commands, also install termux-api:

```bash
pkg install termux-api
```

---

## Commands

### Diagnostics

```
doctor
check setup
```
Checks Python version, ffmpeg, Pillow, allowed paths, and the history-log directory.
No files are touched.

---

### Storage

```
storage report /sdcard/Download
storage report
```
Shows total size, file count, free space, and a breakdown by file category.

```
list large files /sdcard/Download
list large files /sdcard/Download top 10
```
Lists the biggest files, sorted by size.

---

### Browse

```
show files in /sdcard/Download
show files in /sdcard/Download sorted by size
show files in /sdcard/Download sorted by modified
```
Lists every file and folder in a directory.

```
list media in /sdcard/Download
list media in /sdcard/Pictures
list media in /sdcard recursively
show photos in /sdcard/Pictures
find videos in /sdcard/Movies
```
Lists images, videos, and audio files grouped by category with sizes.
No confirmation needed — read-only.

---

### Find

```
find duplicates /sdcard/Download
find duplicates
```
Scans for byte-identical files using SHA-256 hashing. Reports wasted space.
No files are deleted.

---

### Organise

```
organize /sdcard/Download
sort files /sdcard/Download
```
Moves files into category subfolders: `images/`, `videos/`, `documents/`, `audio/`, `archives/`, `code/`, `apks/`, `other/`.

**Preview first. Asks for confirmation. Medium risk.**

---

### Backup

```
back up /sdcard/Documents to /sdcard/Backup
```
Copies a folder to a timestamped backup directory.

**Preview first. Asks for confirmation. Medium risk.**

---

### Convert

```
convert /sdcard/Movies/film.mp4 to mp3
extract audio from /sdcard/Movies/talk.mkv
```
Extracts audio from a video and saves it as an MP3. Requires ffmpeg.

**Preview first. Asks for confirmation. Medium risk.**

---

### Compress

```
compress images /sdcard/Pictures
compress images /sdcard/Pictures quality 60
```
Re-saves images at lower JPEG quality (overwrites originals). Requires Pillow.

**Preview first. Asks for confirmation. HIGH risk — originals are overwritten.**

---

### Rename

```
rename files /sdcard/Download prefix bak_
rename files /sdcard/Download suffix _old
```
Adds a prefix or suffix to every filename in a folder.

**Preview first. Asks for confirmation. HIGH risk.**

---

### Move

```
move /sdcard/Download/report.pdf to /sdcard/Documents
```
Moves a file or folder to a new location.

**Preview first. Asks for confirmation. Medium risk.**

---

### History

```
history
```
Shows your 20 most recent commands with status, timestamp, and intent.

---

### Phone — status

```
show battery status
battery level
```
Returns battery percentage, charging status, health, and temperature.
Requires `termux-api` (`pkg install termux-api`). Read-only, no confirmation.

```
show network status
wifi status
connection info
```
Returns SSID, IP address, link speed, and signal strength.
Requires `termux-api`. Read-only, no confirmation.

---

### Phone — launch app

```
open chrome
open settings
open files
open camera
open gallery
open calculator
```
Launches a supported Android app using a safe, fixed command.
Unsupported app names are rejected with a list of supported names.
**No confirmation required.** Medium risk.

---

### Phone — open URL

```
open https://example.com
visit https://github.com
go to https://wikipedia.org
```
Opens a URL in the default browser via `termux-open-url`.
Only `https://` and `http://` schemes are accepted.
`javascript:`, `file:`, `intent:`, `data:` and similar are always blocked.
**Asks for confirmation before opening.**

---

### Phone — open file

```
open file /sdcard/Download/report.pdf
open /sdcard/Pictures/photo.jpg
```
Opens a local file in the appropriate app via `termux-open`.
Path must be inside the allowed roots in `config/allowed_paths.json`.
**Asks for confirmation before opening.**

---

### Browser — search

```
search for local llm tools
google android tips
look up termux api commands
```
Constructs a Google search URL and opens it in the default browser.
No web request is made by Nabd itself — the browser handles it.
No confirmation required.

---

### Browser — extract text

```
extract text from https://example.com
get text from https://example.com
read page from https://example.com
```
Fetches the URL using Python's standard `urllib` (no external libraries) and
returns the page's readable text with HTML tags stripped.
Returns up to 3,000 characters. Read-only, no confirmation.

**Limitations:**
- Only works on publicly accessible pages (no login required).
- Does not execute JavaScript — fetches raw HTML only.
- Some pages may block automated fetches.

---

### Browser — list links

```
list links from https://example.com
find links on https://example.com
```
Fetches the URL and returns all unique links found in `<a href>` elements.
Returns up to 50 links. Relative links are resolved to absolute URLs.
Read-only, no confirmation.

---

## Session example

```
nabd> doctor

  ✓  Python version              Python 3.11.14 (supported)
  ✓  ffmpeg                      /data/data/com.termux/files/usr/bin/ffmpeg
  ✓  Pillow (image compression)  version 10.4.0
  ✓  Allowed paths               2/2 paths reachable
  ✓  History log directory       /data/.../nabd/data (writable)

  Summary: 5 ok. All checks passed.

nabd> list media in /sdcard/Download

  Directory : /sdcard/Download
  Total     : 47 media file(s), 1.2 GB

  Images  : 31 file(s), 450.0 MB
  Videos  : 12 file(s), 700.0 MB
  Audio   :  4 file(s), 50.0 MB

nabd> find duplicates /sdcard/Download

  Duplicate groups : 3
  Duplicate files  : 8
  Wasted space     : 120.5 MB

  Group 1  (35.2 MB × 3 copies):
    • /sdcard/Download/video.mp4
    • /sdcard/Download/video_copy.mp4

nabd> organize /sdcard/Download

  Would move  : 143 file(s)
    report.pdf  →  documents/
    photo.jpg   →  images/
    ...

  [MEDIUM RISK] Apply these changes? [y/n]: y

  ✓ 143 files moved.
```

---

## Safety model

All paths are validated against `config/allowed_paths.json` before any operation.

Default allowed roots:

```json
{
  "allowed_roots": [
    "/sdcard/Download",
    "/sdcard/Documents",
    "/sdcard/Pictures",
    "/sdcard/Music",
    "/sdcard/Movies",
    "/sdcard/Backup"
  ]
}
```

Edit this file to add or remove directories.
Path traversal sequences (`..`, `//`, `%2e`) are blocked unconditionally.
The executor uses an explicit function whitelist — only declared tool functions can be called.

---

## Architecture

```
main.py  (CLI loop)
  └── agent/
        parser.py    — command → ParsedIntent (regex, English)
        safety.py    — path validation + intent-level guards
        planner.py   — ParsedIntent → ExecutionPlan + ToolActions
        executor.py  — ExecutionPlan → ExecutionResult (whitelist enforced)
        reporter.py  — ExecutionResult → human-readable text

  tools/
        storage.py   — storage_report, list_large_files
        files.py     — organize, rename, move, show_files, list_media
        media.py     — convert_video_to_mp3, compress_images
        backup.py    — backup_folder
        duplicates.py— find_duplicates
        system.py    — run_doctor

  core/
        config.py    — load settings + allowed_paths
        paths.py     — resolve + validate paths
        logging_db.py— SQLite history log, is_first_run()
        exceptions.py— typed exception hierarchy
```

---

## Running tests

```bash
cd nabd
python -m pytest tests/ -v
```

397 tests across `test_parser.py`, `test_tools.py`, `test_safety.py`, `test_planner.py`, `test_executor.py`, `test_integration.py`, `test_v2.py`, and `test_v4_phone_browser.py`.

---

## Troubleshooting

### "Path is outside all allowed directories"

Nabd only operates inside the paths listed in `config/allowed_paths.json`.

```bash
# Check which paths are allowed:
cat config/allowed_paths.json

# Add a new root — edit the file and add your path:
nano config/allowed_paths.json
```

Also confirm that storage permission has been granted in Termux:

```bash
termux-setup-storage
```

---

### "No media found" but files exist in subfolders

By default `list media` only checks the top-level folder. Add `recursively` to scan subfolders:

```
list media in /sdcard/Download recursively
```

Nabd will also show this hint automatically when it detects subdirectories in the scanned folder.

---

### ffmpeg not found

The `convert video to mp3` command requires ffmpeg. Install it with:

```bash
pkg install ffmpeg
```

Then run `doctor` inside Nabd to confirm it is found.

---

### Pillow not found

The `compress images` command requires Pillow. Install it with:

```bash
pip install Pillow
```

---

### "I typed `ls` and nothing happened"

Nabd is not a shell. Common shell commands (`ls`, `cd`, `find`, `mv`, `rm`, …) will show a friendly message with the Nabd equivalent instead of executing.

| Shell | Nabd |
|---|---|
| `ls /sdcard/Download` | `show files in /sdcard/Download` |
| `find /sdcard/Download` | `find duplicates /sdcard/Download` |
| `du /sdcard/Download` | `storage report /sdcard/Download` |
| `mv file.txt /sdcard/Docs` | `move /sdcard/Download/file.txt to /sdcard/Documents` |
| `cp -r /sdcard/Docs /sdcard/Bak` | `back up /sdcard/Documents to /sdcard/Backup` |

Type `exit` to return to Termux for direct shell use.

---

### History database error

If the history log can't be written, check that the `data/` directory inside the Nabd folder is writable. Run `doctor` to confirm.

---

## Changelog

### v0.4
- **Phone status**: `show battery status`, `show network status` (via termux-api JSON)
- **Launch app**: `open chrome`, `open settings`, `open files`, `open camera`, `open gallery`, `open calculator` — fixed safe allowlist, no arbitrary packages
- **Open URL**: `open https://...` — validates scheme (https/http only), blocks javascript:, file:, intent:, data:, vbscript:; confirms before opening
- **Open file**: `open file /path` — validates against allowed roots; confirms before opening
- **Browser search**: `search for X`, `google X`, `look up X` — constructs search URL, opens in browser
- **Browser extract text**: `extract text from https://...` — stdlib fetch, HTML tag stripping, up to 3,000 chars
- **Browser list links**: `list links from https://...` — stdlib fetch, deduped, relative → absolute, up to 50 links
- Extended `ParsedIntent` with `url`, `app_name`, `query` fields
- Extended `ExecutionResult` with `opened_target`, `extracted_text_summary`, `listed_links` fields
- Executor whitelist extended with `phone` and `browser` tool modules
- 130 new tests in `test_v4_phone_browser.py` (397 total)
- README updated with Phone and Browser sections

### v0.2.1
- `list media` now shows a hint suggesting `recursively` when 0 media are found but subfolders exist
- Shell commands (`ls`, `cd`, `mkdir`, `find`, `rm`, `mv`, …) now return a friendly message with the Nabd equivalent instead of "command not recognised"
- `help` includes a "Nabd vs Termux shell" quick-reference table
- New troubleshooting section in README
- 30 new tests (267 total)

### v0.2.0
- `doctor` command — checks Python, ffmpeg, Pillow, allowed paths, history log
- `show files in <path>` — browse directory with sort-by-name/size/date options
- `list media in <path>` — list images/videos/audio grouped by type; supports `recursively`
- Improved `help` output: grouped by category with practical examples
- First-run onboarding message (shown once on a fresh install)
- Better error messages with actionable hints per intent
- Duplicate-results UX: first 5 groups shown in full, remainder summarised
- `history` command: table view with status icons and timestamps
- 82 new tests in `test_v2.py`

### v0.1.0
- Initial release: storage report, large files, organise, find duplicates, backup, video→MP3, compress images, rename, move
- Safety-first: path allowlist, traversal blocking, dry-run preview, explicit confirmation
- SQLite history log
- 155 tests

---

## Roadmap

- Optional local LLM integration for more flexible command parsing
- Recursive duplicate deletion with keeper selection
- Undo / rollback for recent modifying operations
- Scheduled operations (cron-style)
- Rich terminal UI (Textual)
- Smart photo deduplication (perceptual hashing)
- WhatsApp media cleanup tool
- Export history to CSV

---

## License

MIT
