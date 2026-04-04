from typing import Any

from .registry import register_raw_detail
from .shared import append_ai_result


@register_raw_detail("show_skills")
def render_show_skills(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    skills = raw.get("skills", [])
    count = len(skills)
    lines.append(f"\n  {count} skill(s) registered\n")
    for skill in skills:
        status = "✓ enabled " if skill.get("enabled") else "- disabled"
        name = skill.get("name", "?")
        version = skill.get("version", "?")
        desc = skill.get("description", "")
        tags = ", ".join(skill.get("tags", []))
        lines.append(f"  {status}  {name:<20} v{version}")
        lines.append(f"              {desc}")
        if tags:
            lines.append(f"              Tags: {tags}")
        lines.append("")
    if count == 0:
        lines.append("  No skills registered.")


@register_raw_detail("skill_info")
def render_skill_info(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    err = raw.get("error")
    if err:
        lines.append(f"\n  ✗  {err}")
        lines.append("     Type 'show skills' to see available skills.")
        return
    skill = raw.get("skill", {}) or {}
    name = skill.get("name", "?")
    version = skill.get("version", "?")
    desc = skill.get("description", "")
    enabled = skill.get("enabled", False)
    tags = ", ".join(skill.get("tags", []))
    lines.append(f"\n  Name       : {name}")
    lines.append(f"  Version    : v{version}")
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


@register_raw_detail("ai_backend_status")
def render_ai_backend_status(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
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
    elif backend == "ollama":
        endpoint = raw.get("endpoint", "?")
        lines.append(f"  Endpoint : {endpoint}")
        model = raw.get("model_name", "")
        if model:
            lines.append(f"  Model    : {model}")
        timeout = raw.get("timeout_seconds")
        if timeout:
            lines.append(f"  Timeout  : {timeout}s")

    caps = raw.get("capabilities", [])
    if caps:
        lines.append(f"  Capable  : {', '.join(caps)}")

    if detail:
        lines.append(f"  Detail   : {detail}")

    if not enabled:
        lines.append("")
        lines.append('  To enable: edit config/ai_assist.json → "enabled": true')

    troubleshooting = raw.get("troubleshooting")
    if troubleshooting and not available:
        lines.append("")
        lines.append("  Troubleshooting:")
        for hint_line in troubleshooting.splitlines():
            lines.append(f"    {hint_line}")
    elif not available and backend == "llama_cpp":
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


@register_raw_detail("ai_suggest_command", "ai_explain_last_result", "ai_clarify_request")
def render_ai_result(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    append_ai_result(lines, raw)
