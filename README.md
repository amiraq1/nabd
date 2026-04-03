# Nabd

A local-first, safety-first phone operations agent for Android/Termux.

Nabd accepts natural-language commands in English, turns them into structured intents, validates every path against an allowlist, shows a preview before making any change, and asks for confirmation before anything modifying is applied.

**v0.8** — current release.

---

## Design principles

- **Local-only** — no cloud, no network, no external AI APIs required for core functionality.
- **Safety first** — every path is checked against `config/allowed_paths.json`. Path traversal (`..`) is blocked at the parser. URL schemes are validated (only `https://` and `http://` permitted).
- **Preview before apply** — modifying operations run a dry-run pass first. If the preview fails, Nabd stops before confirmation or any real changes.
- **Explicit confirmation** — you type `y` before any file is moved, renamed, or overwritten, or before a URL/file is opened.
- **Whitelisted execution** — only explicitly declared tool functions can be called. No arbitrary shell execution. No arbitrary browser automation.
- **Advisory AI suggestions only** — Nabd can suggest safe next commands based on the current result, recent history, and short-term safe session context, but it never auto-runs them.
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

```
history search <term>
history intent <intent>
history show <id>
```
Filters and re-displays entries from the local history log. These commands only read the log; they never replay or execute past actions. Invalid intent names or missing IDs raise a clarification message before anything changes.

---

### Session context

Nabd keeps a small in-memory context for the current session only.
It can reuse a recent unambiguous folder, URL, or result reference for simple follow-ups such as:

```text
show files in that folder
list large files in it
list links from it
explain that result
```

Use these phrases whenever Nabd just described a folder, media scan, or browser result—the parser only accepts them when the reference is unambiguous, so Nabd will ask for clarification instead of guessing.

Rules:

- context is not persisted across restarts
- context carryover is advisory and parser-side only
- only unambiguous references are reused
- if Nabd is not sure what `it` refers to, it asks you to clarify instead of guessing
- all resolved commands still go through the normal parse → safety → planner → executor flow

---

### Advisory suggestions

After a command finishes, Nabd may show a short `ADVISORY SUGGESTIONS` block.
These suggestions are:

- informational only
- never auto-executed
- generated after the normal parse → safety → planner → executor flow
- based on the current result, recent command history, and current-session safe context when available
- filtered so recent history is only reused when embedded paths stay inside allowed roots and reused URL commands still pass Nabd's URL-safety checks

Examples:

```text
ADVISORY SUGGESTIONS
  - Review the biggest files next: list large files /sdcard/Download
  - Check duplicates in the same folder: find duplicates /sdcard/Download
```

Typical advisory follow-ups include:

- storage and folder-inspection next steps such as `list large files`, `find duplicates`, `list media`, and `show files`
- browser follow-ups such as `list links from ...` after a successful text extract
- environment recovery hints after `doctor` or TLS / Termux integration failures
- post-change review commands such as checking a backup folder or reviewing a destination folder after a move

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
        advisor.py   — advisory-only next-step suggestions from result + history
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

Tests cover parser, safety, planner, executor, tools, phone/browser flows, TLS handling, advisory suggestions, and session-context carryover.

---

### v0.8

- Short-term in-memory session context for the current Nabd session
- Safe follow-up references for simple unambiguous phrases such as `that folder`, `it`, and `that result`
- Clarification-first behavior when context is ambiguous instead of guessing
- Advisory suggestions improved with last command, last result, and recent safe session context
- No auto-execution added; all existing safety checks and confirmation rules remain in place

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

### HTTPS fetch / certificate errors on Termux

`browser_extract_text` and `browser_list_links` make direct HTTPS requests from Python.  
If you see an error like:

```
SSL: CERTIFICATE_VERIFY_FAILED
Verify return code: 20 (unable to get local issuer certificate)
```

this means Python cannot verify the server's certificate because the local CA trust bundle is missing or incomplete. This is a common Termux environment issue — it does **not** affect opening URLs or running searches (those delegate to the Android browser).

**What is NOT affected:**

```
open https://example.com        → still works (Android browser handles TLS)
search for latest news          → still works (Android browser handles TLS)
```

**What IS affected:**

```
extract text from https://...   → requires valid CA certs in Python's SSL stack
list links from https://...     → requires valid CA certs in Python's SSL stack
```

**Fix:**

```bash
# Install the CA certificate bundle for Termux:
pkg install ca-certificates

# Confirm with Nabd's built-in check:
doctor
```

After running `pkg install ca-certificates`, restart Nabd and run `doctor` — the `HTTPS / CA certificates` check should show `ok`.

**Why certificate verification is always on:**  
Nabd never disables certificate verification (`ssl.CERT_NONE`, `verify=False`, or equivalent). Disabling verification would silently expose all HTTPS fetches to man-in-the-middle attacks. The correct fix is to install a proper CA bundle, not to bypass verification.

---

## Changelog

### v0.7
- **llama.cpp backend** (`llm/llama_cpp_backend.py`): optional real local LLM backend via the llama.cpp HTTP server's OpenAI-compatible API (`/v1/chat/completions`); stdlib only (`urllib`, `json`, `socket`) — no new dependencies
- **Structured JSON prompts** (`llm/prompts.py`): every prompt requests a strict JSON object with an explicit schema (`SUGGEST_COMMAND_JSON_TEMPLATE`, `EXPLAIN_RESULT_JSON_TEMPLATE`, `CLARIFY_REQUEST_JSON_TEMPLATE`, `SUGGEST_INTENT_JSON_TEMPLATE`); `LLAMA_SYSTEM_PROMPT` reminds the model it is advisory-only on every call
- **Full response validation**: missing fields, invalid JSON, non-dict content, empty required strings — all caught and replaced with a safe fallback; Nabd never propagates a model error to the user
- **Unsupported intent rejection** (defense in depth): `LlamaCppBackend.suggest_intent` applies its own safety gate in addition to the `AIAssistSkill` gate — any intent not in `allowed_intents` sets `intent=None`, `confidence=0.0`
- **Backend selection via config**: `config/ai_assist.json` now has a `"llama_cpp"` block (`server_url`, `timeout_seconds`, `model_name`); set `"backend": "llama_cpp"` to switch; `"local"` remains the default
- **`ai_backend_status` intent** (new): "ai backend status" / "show ai backend" / "ai status" — shows which backend is active, whether the server is reachable, URL, model, timeout, and a startup hint when the server is down; safe to run at any time, even when AI Assist is disabled
- **`get_backend_status()` method** on `AIAssistSkill`: returns a typed dict; local backend always reports available; llama.cpp probes the server and reports reachable/unreachable with helpful detail
- **Graceful failure modes**: server not running → `is_available()` false; timeout → fallback result with timeout note; invalid JSON → fallback result with error note; missing fields → fallback result; Nabd continues normally in all cases
- **Skill version bumped to 0.2.0**
- **`BackendStatus` dataclass** (`llm/schemas.py`): fifth output schema — `available`, `backend_name`, `transport`, `healthy`, `detail`; returned by `get_status()` on every backend
- **Abstract `get_status()` method** (`llm/backend.py`): every backend must implement it; `LocalBackend` always returns healthy; `LlamaCppBackend` server mode probes `/health`, CLI mode checks file existence
- **CLI transport mode** (`llm/llama_cpp_backend.py`): `"transport": "cli"` calls `llama-cli` as a subprocess with a fixed argument list (`shell=False`), captures stderr, uses `timeout`, parses JSON from stdout; binary and model path absence is reported clearly before execution
- **Configurable `max_tokens` and `temperature`** in `_chat_server()`; both are taken from config and sent in every request rather than being hardcoded
- **Config expanded to full spec §8** (`config/ai_assist.json`): `transport`, `endpoint` (replaces `server_url`), `binary_path`, `model_path`, `max_tokens`, `temperature`
- **147 new tests** in `tests/test_v7_llama_cpp.py` (827 total)

#### Configuring llama.cpp backend (server mode)

1. Edit `config/ai_assist.json`:
```json
{
  "enabled": true,
  "backend": "llama_cpp",
  "mode": "assist_only",
  "fallback_intent_suggestion": false,
  "llama_cpp": {
    "transport": "server",
    "endpoint": "http://127.0.0.1:8080",
    "binary_path": "",
    "model_path": "",
    "timeout_seconds": 20,
    "max_tokens": 256,
    "temperature": 0.2
  }
}
```
2. Start the llama.cpp server:
```
./server -m model.gguf --port 8080 --host 127.0.0.1
```
3. Verify: run `ai backend status` in Nabd.

#### Configuring llama.cpp backend (CLI mode)

Use CLI mode when you cannot run a persistent server (slower, but works anywhere):

```json
{
  "enabled": true,
  "backend": "llama_cpp",
  "mode": "assist_only",
  "fallback_intent_suggestion": false,
  "llama_cpp": {
    "transport": "cli",
    "binary_path": "/data/data/com.termux/files/usr/bin/llama-cli",
    "model_path": "/sdcard/models/model.gguf",
    "timeout_seconds": 60,
    "max_tokens": 256,
    "temperature": 0.2
  }
}
```

CLI mode safety contract: the binary is called with a fixed argument list (`shell=False`); user text is never interpolated into a shell command. If the binary or model file is missing, Nabd reports the error clearly and continues operating normally.

If both server and CLI are configured, server mode is preferred (set `"transport": "server"`).

#### Backend modes

| Setting | Behaviour |
|---|---|
| `"backend": "local"` | Deterministic keyword matching. Always available, no server needed (default). |
| `"backend": "llama_cpp"` | Real LLM via local HTTP server. Falls back gracefully if server is down. |

#### Safety guarantees (v0.7)

- The llama.cpp backend is advisory-only — it never executes actions
- All intents suggested by the model are validated against `AVAILABLE_INTENTS`; non-whitelisted intents are silently discarded at two independent gates (backend + skill)
- Confidence values from the model are clamped to `[0.0, 1.0]`
- `response_format: {"type": "json_object"}` is requested so the model outputs structured JSON; if it fails, Nabd falls back gracefully
- AI failures never break Nabd's deterministic parser/safety/planner/executor pipeline

#### Output schema

All backend responses are typed dataclasses (`llm/schemas.py`):

| Class | Fields |
|---|---|
| `CommandSuggestion` | `suggested_command`, `rationale`, `confidence` |
| `ResultExplanation` | `summary`, `safety_note`, `suggested_next_step` |
| `Clarification` | `clarification_needed`, `clarification_question`, `candidate_intents` |
| `IntentSuggestion` | `intent`, `confidence`, `explanation` |
| `BackendStatus` | `available`, `backend_name`, `transport`, `healthy`, `detail` |

#### Troubleshooting llama.cpp

| Symptom | Fix |
|---|---|
| `ai backend status` shows "✗ unreachable" (server) | Start llama.cpp: `./server -m model.gguf --port 8080` |
| `ai backend status` shows "✗ unreachable" (CLI) | Set `binary_path` and `model_path` in `config/ai_assist.json` |
| CLI: `binary not found` | Install llama.cpp and set the full path to `llama-cli` |
| CLI: `model file not found` | Download a GGUF model and set `model_path` |
| Suggestions always fall back to "doctor" | Backend down or returned bad JSON. Check server logs or model output. |
| Timeout errors | Increase `timeout_seconds` or use a faster/smaller model. |
| `"enabled": false` in status | Edit `config/ai_assist.json` and set `"enabled": true` |
| Invalid JSON from model | Model too small or temperature too high; lower `temperature` to 0.1-0.2. |

### v0.6
- **Skills registry**: `skills/registry.py` — lazy singleton managing all Nabd skill modules; `show skills` and `skill info <name>` expose it to users
- **AI Assist skill** (`skills/ai_assist_skill.py`): advisory-only, never auto-executes; reads `config/ai_assist.json` (default: `enabled: false`); `suggest_command`, `explain_result`, `clarify_request`, `suggest_intent`
- **LocalBackend** (`llm/local_backend.py`): deterministic keyword-overlap matching, no ML, no network; always available; confidence scores are ratios (0.0–1.0), not neural probabilities
- **LLM schemas** (`llm/schemas.py`): typed dataclasses — `CommandSuggestion`, `ResultExplanation`, `Clarification`, `IntentSuggestion`
- **5 new intents** (all `READ_ONLY`, `LOW` risk, no confirmation): `show_skills`, `skill_info` (query=skill\_name), `ai_suggest_command` (query=user\_text), `ai_explain_last_result`, `ai_clarify_request` (query=user\_text)
- **Executor skill routing**: `tool_name="skill"` → `_execute_skill_action`; `tool_name="ai_skill"` → `_execute_ai_skill_action`; both bypass the whitelisted-functions dict
- **Session state** in `main.py`: `_session` dict tracks `last_command`/`last_result`; injected into `ai_explain_last_result` options before planning; not updated for AI meta-commands
- **Fallback intent safety gate**: `suggest_intent()` silently discards any intent not in `AVAILABLE_INTENTS` — backend can never invent non-whitelisted actions
- **Help text updated to v0.6**: SKILLS section + AI ASSIST section with advisory disclaimer and enable instructions
- **Config files**: `config/ai_assist.json` (enabled=false, backend=local, mode=assist\_only) and `config/skills.json`
- **122 new tests** in `tests/test_v6_ai_skill.py` covering registry, parser, safety, planner, executor, reporter, LocalBackend, AIAssistSkill, and safety gate (680 total)

### v0.4.2
- **`show folders` intent**: `show folders in /path` — lists only immediate subfolders (no files), sorted alphabetically, each showing a best-effort item count
- **`browser_page_title` intent**: `show page title from https://...` — fetches a URL via stdlib and returns the `<title>` tag content; full TLS-resilience (same `error_type="tls"` path as `browser_extract_text`)
- **`_TitleExtractor` class** in `tools/browser.py`: stateful `html.parser.HTMLParser` that captures the first `<title>` tag, handles entities, collapses whitespace, stops at `</title>`
- **`python` shell-command hint**: typing `python` inside Nabd now shows "Nabd does not run Python scripts — use Termux for scripting: `python3 script.py`"
- **Help text updated to v0.4.2**: new `show folders` example under BROWSE; new `show page title from` example under BROWSER; `add 'recursively'` tip added to `list media`; Shell → Nabd table extended with `ls -d */`, `python`
- Safety, planner, executor, reporter all wired for both new intents (read-only, LOW risk, no confirmation required)
- 99 new tests in `test_v4_2.py` (552 total)

### v0.4.1
- **Browser TLS resilience**: `_fetch_html` now distinguishes SSL certificate errors (`error_type="tls"`) from plain network failures (`error_type="network"`)
- **Actionable TLS error messages**: when `extract text` or `list links` fails with an SSL error, Nabd shows the exact fix (`pkg install ca-certificates`) and confirms that `open https://...` and `search for` are unaffected
- **Browser-safe fallback UX**: when `browser_extract_text` or `browser_list_links` fails with a TLS error, Nabd presents a structured environment-error block with two concrete alternative commands — `open <the-exact-url>` and `search for <domain>` — so the user can act immediately without needing to edit anything; the block also states the fix and that `open`/`search` are unaffected
- **`check_browser_tls()` function** added to `tools/browser.py`: attempts a real HTTPS fetch, distinguishes TLS failures from offline/no-network states, returns `{"status": "ok"|"warn"|"error", "detail": "..."}`
- **Doctor check #6** (`HTTPS / CA certificates`): `run_doctor()` now calls `check_browser_tls()` and reports TLS status alongside the existing five checks
- **`error_type` field** added to all three browser tool result dicts (`browser_extract_text`, `browser_list_links`, `browser_search`)
- **`_is_tls_error()` helper** centralises SSL exception detection across `urllib.error.URLError`, `ssl.SSLCertVerificationError`, `ssl.SSLError`, and string-reason cases
- Certificate verification is never disabled (no `ssl.CERT_NONE`, no `verify=False` equivalents)
- 56 new tests in `test_v4_1_tls.py` (453 total)
- README: new "HTTPS fetch / certificate errors on Termux" troubleshooting section

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
