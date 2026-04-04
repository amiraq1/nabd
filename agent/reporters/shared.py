from typing import Any


def tls_fallback_lines(url: str) -> list[str]:
    """
    Build the structured TLS environment-error block shown when browser_extract_text
    or browser_list_links fails due to SSL certificate verification.

    Shows concrete alternative commands using the actual URL the user provided,
    so they can take action immediately without editing the suggestion.
    """
    domain = url
    for prefix in ("https://", "http://"):
        if domain.startswith(prefix):
            domain = domain[len(prefix):]
            break
    domain = domain.split("/")[0]

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


def append_ai_result(lines: list[str], raw: dict[str, Any]) -> None:
    """Format AI Assist results — suggest_command, explain_result, clarify_request."""
    success = raw.get("success", True)
    ai_type = raw.get("type", "")

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
        is_fallback = confidence == 0.0 and "unavailable" in rationale.lower()
        if is_fallback:
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
