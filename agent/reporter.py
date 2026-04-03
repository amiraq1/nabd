from typing import Any

from agent.models import ExecutionPlan, OperationStatus, ParsedIntent
from tools.utils import truncate_list

SEPARATOR = "─" * 55


def _section(title: str) -> str:
    return f"\n{SEPARATOR}\n  {title}\n{SEPARATOR}"


def _tls_fallback_lines(url: str) -> list[str]:
    """
    Build the structured TLS environment-error block shown when browser_extract_text
    or browser_list_links fails due to SSL certificate verification.

    Shows concrete alternative commands using the actual URL the user provided,
    so they can take action immediately without editing the suggestion.
    """
    # Extract bare domain for the 'search for' suggestion
    domain = url
    for prefix in ("https://", "http://"):
        if domain.startswith(prefix):
            domain = domain[len(prefix):]
            break
    domain = domain.split("/")[0]   # strip any path

    return [
        f"\n  ✗  SSL certificate error — cannot fetch {url}",
        "     Local CA trust store is missing or incomplete.",
        "     This is an environment issue, not a problem with the URL.",
        "",
        "     Try these instead (no local TLS needed):",
        f"       open {url}",
        f"       search for {domain}",
        "",
        "     Fix (Termux):  pkg install ca-certificates",
        "     Verify:        run 'doctor'",
    ]


def report_parsed_intent(intent: ParsedIntent) -> str:
    lines = [_section("PARSED INTENT")]
    lines.append(f"  Intent       : {intent.intent}")
    lines.append(f"  Source Path  : {intent.source_path or '(not specified)'}")
    if intent.target_path:
        lines.append(f"  Target Path  : {intent.target_path}")
    lines.append(f"  Risk Level   : {intent.risk_level.value.upper()}")
    lines.append(f"  Needs Confirm: {'Yes' if intent.requires_confirmation else 'No'}")
    if intent.options:
        for k, v in intent.options.items():
            lines.append(f"  Option [{k}] : {v}")
    return "\n".join(lines)


def report_plan(plan: ExecutionPlan) -> str:
    lines = [_section("EXECUTION PLAN")]
    lines.append(f"  Intent     : {plan.intent}")
    lines.append(f"  Risk Level : {plan.risk_level.value.upper()}")
    lines.append(f"  Dry Run    : {'Yes — preview only, no changes' if plan.dry_run else 'No'}")
    lines.append(f"  Preview    : {plan.preview_summary}")
    lines.append(f"  Actions    : {len(plan.actions)}")
    for i, action in enumerate(plan.actions, 1):
        lines.append(f"    [{i}] {action.tool_name}.{action.function_name}({_fmt_args(action.arguments)})")
    return "\n".join(lines)


def _fmt_args(args: dict) -> str:
    parts = []
    for k, v in args.items():
        if isinstance(v, str) and len(v) > 40:
            v = v[:37] + "..."
        parts.append(f"{k}={v!r}")
    return ", ".join(parts)


def report_result(result: Any, intent_name: str, confirmed: bool) -> str:
    lines = [_section("RESULT")]
    status_icon = {
        OperationStatus.SUCCESS: "✓",
        OperationStatus.FAILURE: "✗",
        OperationStatus.PARTIAL: "~",
        OperationStatus.SKIPPED: "-",
        OperationStatus.CANCELLED: "⊘",
    }.get(result.status, "?")

    lines.append(f"  Status  : {status_icon} {result.status.value.upper()}")
    lines.append(f"  Message : {result.message}")

    raw_results = getattr(result, "raw_results", [])
    for raw in raw_results:
        _append_raw_details(lines, raw, intent_name, confirmed)

    if result.affected_paths:
        shown, extra = truncate_list(result.affected_paths, 10)
        lines.append("\n  Affected Paths:")
        for p in shown:
            lines.append(f"    • {p}")
        if extra:
            lines.append(f"    ... and {extra} more")

    if result.errors:
        lines.append("\n  Errors:")
        for e in result.errors:
            lines.append(f"    ! {e}")

    return "\n".join(lines)


def _append_raw_details(lines: list, raw: dict, intent: str, confirmed: bool) -> None:

    if intent == "doctor":
        checks = raw.get("checks", [])
        ok = raw.get("ok_count", 0)
        warn = raw.get("warn_count", 0)
        err = raw.get("error_count", 0)
        overall = raw.get("overall", "?")

        icon_map = {"ok": "✓", "warn": "⚠", "missing": "✗", "error": "✗"}
        lines.append("")
        for check in checks:
            icon = icon_map.get(check["status"], "?")
            lines.append(f"  {icon}  {check['name']:<30} {check['detail']}")

        lines.append("")
        summary_parts = [f"{ok} ok"]
        if warn:
            summary_parts.append(f"{warn} warning{'s' if warn != 1 else ''}")
        if err:
            summary_parts.append(f"{err} issue{'s' if err != 1 else ''}")
        overall_label = {"ok": "All checks passed.", "warn": "Ready with warnings.", "error": "Action required."}.get(overall, "")
        lines.append(f"  Summary: {', '.join(summary_parts)}. {overall_label}")

    elif intent == "storage_report":
        lines.append(f"\n  Directory    : {raw.get('directory', '')}")
        lines.append(f"  Total Size   : {raw.get('total_size_human', '')}")
        lines.append(f"  Files        : {raw.get('file_count', 0)}")
        lines.append(f"  Directories  : {raw.get('directory_count', 0)}")
        lines.append(f"  Free Space   : {raw.get('free_space_human', 'N/A')}")
        breakdown = raw.get("category_breakdown", {})
        if breakdown:
            lines.append("  By Category  :")
            for cat, sz in breakdown.items():
                lines.append(f"    • {cat:<14}: {sz}")

    elif intent == "list_large_files":
        files = raw if isinstance(raw, list) else raw.get("files", [])
        lines.append(f"\n  Found {len(files)} large file(s):")
        shown, extra = truncate_list(files, 15)
        for item in shown:
            if isinstance(item, dict):
                lines.append(f"    {item.get('size_human', '?'):>10}  {item.get('path', '')}")
        if extra:
            lines.append(f"    ... and {extra} more large files (use 'list large files <path> limit N' to see more)")

    elif intent == "show_files":
        entries = raw.get("entries", [])
        total = raw.get("total_entries", 0)
        file_count = raw.get("file_count", 0)
        dir_count = raw.get("dir_count", 0)
        truncated = raw.get("truncated", 0)
        sort_by = raw.get("sort_by", "name")

        lines.append(f"\n  Directory : {raw.get('directory', '')}")
        lines.append(f"  Contents  : {file_count} file(s), {dir_count} folder(s)  (sorted by {sort_by})")
        lines.append("")

        shown_entries, extra_entries = truncate_list(entries, 15)
        for entry in shown_entries:
            if entry["is_dir"]:
                lines.append(f"    {'DIR':>10}  📁 {entry['name']}/")
            else:
                lines.append(f"    {entry['size_human']:>10}  {entry['name']}")

        more_count = truncated or (len(entries) - len(shown_entries))
        if more_count > 0:
            lines.append(f"\n    ... and {more_count} more entries (use 'limit N' or a narrower path to see more)")

    elif intent == "list_media":
        summary = raw.get("summary", {})
        groups = raw.get("groups", {})
        total = raw.get("total_media_count", 0)
        total_size = raw.get("total_size_human", "0 B")
        directory = raw.get("directory", "")
        recursive = raw.get("recursive", False)

        lines.append(f"\n  Directory : {directory}{'  (recursive)' if recursive else ''}")
        lines.append(f"  Total     : {total} media file(s), {total_size}")
        lines.append("")

        CATEGORY_LABELS = {"images": "Images", "videos": "Videos", "audio": "Audio"}
        for cat, label in CATEGORY_LABELS.items():
            info = summary.get(cat, {})
            count = info.get("count", 0)
            size = info.get("total_size_human", "0 B")
            items = groups.get(cat, [])
            if count == 0:
                lines.append(f"  {label:<8}: (none)")
                continue
            lines.append(f"  {label:<8}: {count} file(s), {size}")
            shown, extra = truncate_list(items, 10)
            for item in shown:
                lines.append(f"    {item['size_human']:>10}  {item['name']}")
            if extra:
                lines.append(f"    ... and {extra} more")

        if total == 0 and not recursive and raw.get("has_subdirs"):
            lines.append(
                f"\n  Hint: No media found directly in this folder, but subfolders exist."
                f"\n        To scan them too, try:"
                f"\n          list media in {directory} recursively"
            )

    elif intent == "show_folders":
        directory = raw.get("directory", "")
        folder_count = raw.get("folder_count", 0)
        folders = raw.get("folders", [])
        errors = raw.get("errors", [])
        lines.append(f"\n  Directory  : {directory}")
        lines.append(f"  Subfolders : {folder_count}")
        if folder_count == 0:
            lines.append("\n  No subfolders found.")
        else:
            lines.append("")
            shown, extra = truncate_list(folders, 20)
            for folder in shown:
                name = folder.get("name", "")
                count = folder.get("item_count")
                count_str = f"({count} item{'s' if count != 1 else ''})" if count is not None else "(unreadable)"
                lines.append(f"    {name + '/':<30} {count_str}")
            if extra:
                lines.append(f"    ... and {extra} more")
        if errors:
            lines.append("\n  Errors:")
            for e in errors:
                lines.append(f"    ! {e}")

    elif intent == "organize_folder_by_type":
        moves = raw.get("planned_moves", [])
        skipped = raw.get("skipped", [])
        moved = raw.get("moved", [])
        errors = raw.get("errors", [])
        if confirmed:
            lines.append(f"\n  Moved  : {len(moved)} file(s)")
            lines.append(f"  Errors : {len(errors)}")
        else:
            lines.append(f"\n  Would move  : {len(moves)} file(s)")
            lines.append(f"  Would skip  : {len(skipped)} (already in correct folder)")
            shown, extra = truncate_list(moves, 10)
            for m in shown:
                src_name = m.get("source", "").split("/")[-1]
                dest_dir = "/".join(m.get("destination", "").split("/")[:-1]).split("/")[-1]
                lines.append(f"    {src_name}  →  {dest_dir}/")
            if extra:
                lines.append(f"    ... and {extra} more")

    elif intent == "find_duplicates":
        groups = raw.get("duplicate_groups", [])
        total_wasted = raw.get("total_wasted_human", "0 B")
        total_groups = raw.get("total_groups", 0)
        total_files = sum(len(g.get("paths", [])) for g in groups)

        lines.append(f"\n  Duplicate groups : {total_groups}")
        lines.append(f"  Duplicate files  : {total_files}")
        lines.append(f"  Wasted space     : {total_wasted}")

        if total_groups == 0:
            lines.append("\n  No duplicates found.")
            return

        # Show first 5 groups in full, summarize the rest
        shown_groups, extra_groups = truncate_list(groups, 3)
        lines.append("")
        for i, g in enumerate(shown_groups, 1):
            size_human = g.get("file_size_human", "?")
            paths = g.get("paths", [])
            lines.append(f"  Group {i}  ({size_human} × {len(paths)} copies):")
            shown_paths, extra_paths = truncate_list(paths, 4)
            for p in shown_paths:
                lines.append(f"    • {p}")
            if extra_paths:
                lines.append(f"    ... and {extra_paths} more copies")

        if extra_groups:
            lines.append(f"\n  ... and {extra_groups} more group(s) not shown.")
            lines.append("  Tip: run 'find duplicates' with tighter filters to inspect additional groups.")

    elif intent in {"history_search", "history_intent"}:
        entries = raw.get("entries", [])
        count = raw.get("count", len(entries))
        lines.append(f"\n  Found {count} matching history entr{'ies' if count != 1 else 'y'}:")
        shown, extra = truncate_list(entries, 20)
        for entry in shown:
            lines.append(
                f"    [{entry.get('id')}] {entry.get('status', '?'):>9} "
                f"{entry.get('intent', '?'):>15} : {entry.get('command', '')}"
            )
        if extra:
            lines.append(f"    ... and {extra} more entries")

    elif intent == "history_show":
        entry = raw.get("entry")
        if entry:
            ts = entry.get("timestamp", "")[:19].replace("T", " ")
            lines.append(f"\n  Entry [{entry.get('id')}]  {entry.get('status', '?')}  {ts}")
            lines.append(f"  Intent : {entry.get('intent', '')}")
            lines.append(f"  Command: {entry.get('command', '')}")
            if entry.get("error_details"):
                lines.append(f"  Errors : {entry.get('error_details')}")
        else:
            lines.append(f"\n  {raw.get('message', 'No history entry found.')}")

    elif intent == "backup_folder":
        lines.append(f"\n  Source      : {raw.get('source', '')}")
        lines.append(f"  Destination : {raw.get('destination', '')}")
        lines.append(f"  Files       : {raw.get('file_count', 0)}")
        lines.append(f"  Size        : {raw.get('total_size_human', '')}")
        if confirmed and raw.get("success"):
            lines.append("  Backup      : COMPLETED")
        elif not confirmed:
            lines.append(f"  Note        : {raw.get('message', '')}")

    elif intent == "convert_video_to_mp3":
        lines.append(f"\n  Input  : {raw.get('video_path', '')}")
        lines.append(f"  Output : {raw.get('output_path', '')}")
        if not confirmed:
            lines.append(f"  Note   : {raw.get('message', '')}")

    elif intent == "compress_images":
        planned = raw.get("planned", [])
        compressed = raw.get("compressed", [])
        lines.append(f"\n  Quality : {raw.get('quality', '?')}%")
        if confirmed:
            lines.append(f"  Compressed : {len(compressed)} image(s)")
        else:
            lines.append(f"  Would compress : {len(planned)} image(s)")
            if planned:
                shown, extra = truncate_list(planned, 5)
                for p in shown:
                    lines.append(f"    • {p.split('/')[-1]}")
                if extra:
                    lines.append(f"    ... and {extra} more")

    elif intent == "safe_rename_files":
        planned = raw.get("planned_renames", [])
        renamed = raw.get("renamed", [])
        if confirmed:
            lines.append(f"\n  Renamed : {len(renamed)} file(s)")
        else:
            lines.append(f"\n  Would rename : {len(planned)} file(s)")
            shown, extra = truncate_list(planned, 8)
            for r in shown:
                src = r.get("source", "").split("/")[-1]
                dst = r.get("destination", "").split("/")[-1]
                lines.append(f"    {src}  →  {dst}")
            if extra:
                lines.append(f"    ... and {extra} more")

    elif intent == "safe_move_files":
        moved = raw.get("moved", [])
        planned = raw.get("planned", {})
        if confirmed:
            lines.append(f"\n  Moved : {len(moved)} item(s)")
        else:
            if isinstance(planned, dict):
                src = planned.get("source", "").split("/")[-1]
                dst = planned.get("destination", "")
                lines.append(f"\n  Would move : {src}  →  {dst}")

    # ── Phone intents ──────────────────────────────────────────────────────────

    elif intent == "open_app":
        success = raw.get("success", False)
        app_name = raw.get("app_name", "?")
        description = raw.get("description", "")
        if success:
            lines.append(f"\n  ✓  Launched: {app_name}")
            if description:
                lines.append(f"     {description}")
        else:
            error = raw.get("error", "Unknown error")
            supported = raw.get("supported_apps", [])
            lines.append(f"\n  ✗  Could not launch: {app_name}")
            lines.append(f"     {error}")
            if supported:
                lines.append(f"\n  Supported apps: {', '.join(supported)}")

    elif intent == "open_file":
        success = raw.get("success", False)
        path = raw.get("path", "?")
        if success:
            lines.append(f"\n  ✓  Opened: {path}")
        else:
            lines.append(f"\n  ✗  Could not open: {path}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
            lines.append("  Hint: Install termux-api (pkg install termux-api) if termux-open is missing.")

    elif intent == "open_url":
        success = raw.get("success", False)
        url = raw.get("url", "?")
        if success:
            lines.append(f"\n  ✓  Opened in browser: {url}")
        else:
            lines.append(f"\n  ✗  Could not open URL: {url}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
            lines.append("  Hint: Install termux-api (pkg install termux-api) if termux-open-url is missing.")

    elif intent == "phone_status_battery":
        success = raw.get("success", False)
        if not success:
            lines.append(f"\n  ✗  Battery status unavailable.")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
            lines.append("  Hint: Install termux-api: pkg install termux-api")
            return
        percentage = raw.get("percentage", "?")
        status = raw.get("status", "?")
        health = raw.get("health", "?")
        temperature = raw.get("temperature", "?")
        plugged = raw.get("plugged", "?")
        lines.append(f"\n  Battery Level : {percentage}%")
        lines.append(f"  Status        : {status}")
        if health and health != "?":
            lines.append(f"  Health        : {health}")
        if temperature and temperature != "?":
            lines.append(f"  Temperature   : {temperature} °C")
        if plugged and plugged != "?":
            lines.append(f"  Plugged       : {plugged}")

    elif intent == "phone_status_network":
        success = raw.get("success", False)
        if not success:
            lines.append(f"\n  ✗  Network status unavailable.")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
            lines.append("  Hint: Install termux-api: pkg install termux-api")
            return
        ssid = raw.get("ssid", "?")
        ip = raw.get("ip", "?")
        link_speed = raw.get("link_speed_mbps", raw.get("link_speed", "?"))
        signal = raw.get("rssi", "?")
        freq = raw.get("frequency_mhz", raw.get("frequency", "?"))
        lines.append(f"\n  SSID          : {ssid}")
        lines.append(f"  IP Address    : {ip}")
        if link_speed and link_speed != "?":
            lines.append(f"  Link Speed    : {link_speed} Mbps")
        if signal and signal != "?":
            lines.append(f"  Signal (RSSI) : {signal} dBm")
        if freq and freq != "?":
            lines.append(f"  Frequency     : {freq} MHz")

    # ── Browser intents ────────────────────────────────────────────────────────

    elif intent == "browser_search":
        success = raw.get("success", False)
        query = raw.get("query", "?")
        search_url = raw.get("search_url", "")
        if success:
            lines.append(f"\n  ✓  Search opened in browser")
            lines.append(f"     Query : {query}")
        else:
            lines.append(f"\n  ✗  Could not open search for: {query}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
            if search_url:
                lines.append(f"\n  You can copy this URL and open it manually:")
                lines.append(f"     {search_url}")
            lines.append("  Hint: Install termux-api: pkg install termux-api")

    elif intent == "browser_page_title":
        success = raw.get("success", False)
        url = raw.get("url", "?")
        if not success:
            error_type = raw.get("error_type", "")
            if error_type == "tls":
                lines.extend(_tls_fallback_lines(url))
            else:
                lines.append(f"\n  ✗  Could not fetch: {url}")
                error = raw.get("error", "")
                if error:
                    lines.append(f"     {error}")
            return
        title = raw.get("title", "")
        lines.append(f"\n  URL   : {url}")
        lines.append(f"  Title : {title if title else '(no title found)'}")

    elif intent == "browser_extract_text":
        success = raw.get("success", False)
        url = raw.get("url", "?")
        if not success:
            error_type = raw.get("error_type", "")
            if error_type == "tls":
                lines.extend(_tls_fallback_lines(url))
            else:
                lines.append(f"\n  ✗  Could not fetch: {url}")
                error = raw.get("error", "")
                if error:
                    lines.append(f"     {error}")
            return
        text = raw.get("text", "")
        char_count = raw.get("char_count", 0)
        truncated = raw.get("truncated", False)
        lines.append(f"\n  URL      : {url}")
        lines.append(f"  Size     : {char_count} character(s)")
        if truncated:
            lines.append("  (showing first 3,000 characters)")
        lines.append("")
        if text:
            for para in text.split("  "):
                stripped = para.strip()
                if stripped:
                    lines.append(f"  {stripped[:120]}")
        else:
            lines.append("  (no readable text found)")

    # ── Skills system ──────────────────────────────────────────────────────────

    elif intent == "show_skills":
        skills = raw.get("skills", [])
        count = len(skills)
        lines.append(f"\n  {count} skill(s) registered\n")
        for s in skills:
            status = "✓ enabled " if s.get("enabled") else "- disabled"
            name = s.get("name", "?")
            ver = s.get("version", "?")
            desc = s.get("description", "")
            tags = ", ".join(s.get("tags", []))
            lines.append(f"  {status}  {name:<20} v{ver}")
            lines.append(f"              {desc}")
            if tags:
                lines.append(f"              Tags: {tags}")
            lines.append("")
        if count == 0:
            lines.append("  No skills registered.")

    elif intent == "skill_info":
        err = raw.get("error")
        if err:
            lines.append(f"\n  ✗  {err}")
            lines.append("     Type 'show skills' to see available skills.")
            return
        s = raw.get("skill", {}) or {}
        name = s.get("name", "?")
        ver = s.get("version", "?")
        desc = s.get("description", "")
        enabled = s.get("enabled", False)
        tags = ", ".join(s.get("tags", []))
        lines.append(f"\n  Name       : {name}")
        lines.append(f"  Version    : v{ver}")
        lines.append(f"  Status     : {'Enabled' if enabled else 'Disabled'}")
        lines.append(f"  Description: {desc}")
        if tags:
            lines.append(f"  Tags       : {tags}")
        if name == "ai_assist":
            lines.append("")
            lines.append("  Commands:")
            lines.append("    suggest command for <text>")
            lines.append("    explain last result")
            lines.append("    help me with <text>")
            lines.append("")
            lines.append("  Note: AI suggestions are advisory only.")
            lines.append("        Actions are never executed automatically.")
            if not enabled:
                lines.append("")
                lines.append("  To enable:")
                lines.append('    Edit config/ai_assist.json → set "enabled": true')

    # ── AI Assist results ──────────────────────────────────────────────────────

    elif intent == "ai_backend_status":
        err = raw.get("error")
        if err:
            lines.append(f"\n  ✗  {err}")
            return
        backend = raw.get("backend", "?")
        available = raw.get("available", False)
        enabled = raw.get("enabled", False)
        transport = raw.get("transport")
        detail = raw.get("detail", "")
        avail_str = "✓ reachable" if available else "✗ unreachable"
        enabled_str = "Enabled" if enabled else "Disabled"
        lines.append(f"\n  Backend  : {backend}")
        lines.append(f"  Status   : {enabled_str}")
        if transport:
            lines.append(f"  Transport: {transport}")
        lines.append(f"  Reachable: {avail_str}")
        if backend == "llama_cpp":
            transport_mode = transport or "server"
            if transport_mode == "cli":
                lines.append(f"  Binary   : {raw.get('binary_path', '(not set)')}")
                lines.append(f"  Model    : {raw.get('model_path', '(not set)')}")
            else:
                endpoint = raw.get("endpoint") or raw.get("server_url", "?")
                lines.append(f"  Endpoint : {endpoint}")
                model = raw.get("model_name", "")
                if model:
                    lines.append(f"  Model    : {model}")
            timeout = raw.get("timeout_seconds")
            if timeout:
                lines.append(f"  Timeout  : {timeout}s")
            max_tokens = raw.get("max_tokens")
            if max_tokens:
                lines.append(f"  Max toks : {max_tokens}")
            temperature = raw.get("temperature")
            if temperature is not None:
                lines.append(f"  Temp     : {temperature}")
        if detail:
            lines.append(f"  Detail   : {detail}")
        if not enabled:
            lines.append("")
            lines.append('  To enable: edit config/ai_assist.json → "enabled": true')
        if backend == "llama_cpp" and not available:
            transport_mode = transport or "server"
            lines.append("")
            if transport_mode == "cli":
                lines.append("  Set binary_path and model_path in config/ai_assist.json")
                lines.append("  Example:")
                lines.append("    \"binary_path\": \"/data/data/com.termux/files/usr/bin/llama-cli\"")
                lines.append("    \"model_path\": \"/sdcard/models/model.gguf\"")
            else:
                lines.append("  To start llama.cpp server:")
                lines.append("    ./server -m model.gguf --port 8080 --host 127.0.0.1")

    elif intent in ("ai_suggest_command", "ai_explain_last_result", "ai_clarify_request"):
        _append_ai_result(lines, raw)

    elif intent == "browser_list_links":
        success = raw.get("success", False)
        url = raw.get("url", "?")
        if not success:
            error_type = raw.get("error_type", "")
            if error_type == "tls":
                lines.extend(_tls_fallback_lines(url))
            else:
                lines.append(f"\n  ✗  Could not fetch: {url}")
                error = raw.get("error", "")
                if error:
                    lines.append(f"     {error}")
            return
        links = raw.get("links", [])
        link_count = raw.get("link_count", 0)
        lines.append(f"\n  URL        : {url}")
        lines.append(f"  Links found: {link_count}")
        if not links:
            lines.append("  (no links found on this page)")
            return
        lines.append("")
        shown, extra = truncate_list(links, 20)
        for i, link in enumerate(shown, 1):
            href = link.get("url", "?")
            lines.append(f"  {i:>3}.  {href}")
        if extra:
            lines.append(f"\n  ... and {extra} more link(s) not shown.")


def _append_ai_result(lines: list, raw: dict) -> None:
    """Format AI Assist results — suggest_command, explain_result, clarify_request."""
    success = raw.get("success", True)
    ai_type = raw.get("type", "")

    # ── Disabled / error ──────────────────────────────────────────────────────
    if not success:
        error = raw.get("error", "AI Assist is unavailable.")
        lines.append("\n  ✗  AI Assist unavailable.")
        for line in error.strip().splitlines():
            lines.append(f"     {line.strip()}")
        return

    lines.append("\n  ⓘ  Advisory only — no action has been taken.\n")

    if ai_type == "suggest_command":
        cmd = raw.get("suggested_command", "?")
        rationale = raw.get("rationale", "")
        confidence = raw.get("confidence", 0.0)
        pct = int(confidence * 100)
        # Show unavailable warning only for the explicit fallback (confidence==0.0 and
        # rationale contains "unavailable").  Do NOT fire for a genuine model response
        # that happens to report a very low confidence — that is a different case.
        _is_fallback = (confidence == 0.0 and "unavailable" in rationale.lower())
        if _is_fallback:
            lines.append("\n  ⚠  AI backend unavailable — showing safe default, not an AI suggestion.")
            lines.append("     Run 'ai backend status' to check your AI configuration.\n")
        lines.append(f"  Suggested command : {cmd}")
        lines.append(f"  Reason            : {rationale}")
        lines.append(f"  Confidence        : {pct}%")
        lines.append("")
        lines.append("  To run this command, type it exactly as shown above.")

    elif ai_type == "explain_result":
        summary = raw.get("summary", "")
        safety_note = raw.get("safety_note")
        next_step = raw.get("suggested_next_step")
        if summary:
            lines.append(f"  Summary   : {summary}")
        if safety_note:
            lines.append(f"  ⚠ Safety  : {safety_note}")
        if next_step:
            lines.append(f"  Next step : {next_step}")

    elif ai_type == "clarify_request":
        needed = raw.get("clarification_needed", False)
        question = raw.get("clarification_question")
        candidates = raw.get("candidate_intents", [])
        if question:
            lines.append(f"  Question  : {question}")
        if candidates:
            lines.append(f"  Candidates: {', '.join(candidates)}")
        if not needed and not question:
            lines.append("  Your request seems clear. Try typing it as a Nabd command.")
