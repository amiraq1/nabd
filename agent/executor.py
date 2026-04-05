import copy
import importlib
from typing import Any

from agent.models import ExecutionPlan, ExecutionResult, OperationStatus, ToolAction
from core.exceptions import ExecutionError, ToolError


# Whitelisted functions for the skill registry (read-only, no tool module)
_SKILL_FUNCTIONS: set[str] = {"list_skills", "skill_info", "backend_status", "run_skill"}

# Whitelisted functions for the AI Assist skill (advisory only, never executes)
_AI_SKILL_FUNCTIONS: set[str] = {
    "suggest_command",
    "explain_result",
    "clarify_request",
    "suggest_intent",
}

WHITELISTED_FUNCTIONS: dict[str, set[str]] = {
    "system":     {"run_doctor"},
    "storage":    {"get_storage_report", "list_large_files"},
    "files":      {"organize_folder_by_type", "safe_rename_files", "safe_move_files",
                   "show_files", "show_folders", "list_media"},
    "media":      {"convert_video_to_mp3", "compress_images"},
    "backup":     {"backup_folder"},
    "duplicates": {"find_duplicates"},
    "phone":      {"open_app", "open_file", "open_url",
                   "get_battery_status", "get_network_status"},
    "browser":    {"browser_search", "browser_extract_text", "browser_list_links",
                   "browser_page_title"},
    "history":    {"search_history", "history_by_intent", "show_history_entry"},
    "schedule":   {"create_schedule", "list_schedules", "delete_schedule"},
}

_TOOL_MODULES: dict[str, str] = {
    "system": "tools.system",
    "storage": "tools.storage",
    "files": "tools.files",
    "media": "tools.media",
    "backup": "tools.backup",
    "duplicates": "tools.duplicates",
    "phone": "tools.phone",
    "browser": "tools.browser",
    "history": "tools.history",
    "schedule": "tools.schedule",
}


def _execute_skill_action(action: ToolAction) -> dict[str, Any]:
    """Handle skill registry queries — read-only, no tool module needed."""
    if action.function_name not in _SKILL_FUNCTIONS:
        raise ExecutionError(
            f"Skill function '{action.function_name}' is not whitelisted. "
            f"Allowed: {sorted(_SKILL_FUNCTIONS)}"
        )
    from skills.registry import get_registry
    registry = get_registry()

    if action.function_name == "list_skills":
        skills = registry.list_skills()
        return {
            "skills": [
                {
                    "name": s.name,
                    "description": s.description,
                    "version": s.version,
                    "enabled": s.enabled,
                    "tags": s.tags,
                    "path": s.path,
                    "author": s.author,
                    "requires_python": s.requires_python,
                    "has_python_logic": s.has_python_logic,
                    "entrypoint": s.entrypoint,
                    "usage": s.usage,
                    "instructions": s.instructions,
                    "source": s.source,
                }
                for s in skills
            ],
            "load_errors": registry.list_errors(),
        }

    if action.function_name == "skill_info":
        skill_name = action.arguments.get("skill_name", "")
        skill = registry.get_skill(skill_name)
        if skill is None:
            return {"error": f"Unknown skill: '{skill_name}'", "skill": None}
        info = skill.get_info()
        return {
            "skill": {
                "name": info.name,
                "description": info.description,
                "version": info.version,
                "enabled": info.enabled,
                "tags": info.tags,
                "path": info.path,
                "author": info.author,
                "requires_python": info.requires_python,
                "has_python_logic": info.has_python_logic,
                "entrypoint": info.entrypoint,
                "usage": info.usage,
                "instructions": info.instructions,
                "source": info.source,
            }
        }

    if action.function_name == "backend_status":
        skill = registry.get("ai_assist")
        if skill is None:
            return {"error": "AI Assist skill is not registered."}
        return skill.get_backend_status()

    if action.function_name == "run_skill":
        skill_name = action.arguments.get("skill_name", "")
        try:
            return registry.execute_skill(skill_name)
        except RuntimeError as exc:
            return {
                "success": False,
                "skill_name": skill_name,
                "error": str(exc),
            }

    raise ExecutionError(f"Unhandled skill function: '{action.function_name}'")


def _execute_ai_skill_action(action: ToolAction) -> dict[str, Any]:
    """Handle AI Assist skill calls — advisory only, never executes tool actions."""
    if action.function_name not in _AI_SKILL_FUNCTIONS:
        raise ExecutionError(
            f"AI skill function '{action.function_name}' is not whitelisted. "
            f"Allowed: {sorted(_AI_SKILL_FUNCTIONS)}"
        )
    from skills.registry import get_registry
    registry = get_registry()
    skill = registry.get("ai_assist")
    if skill is None:
        return {"success": False, "error": "AI Assist skill is not registered.", "type": action.function_name}

    try:
        fn_name = action.function_name
        if fn_name == "suggest_command":
            r = skill.suggest_command(action.arguments.get("user_text", ""))
            return {
                "success": True, "type": "suggest_command",
                "suggested_command": r.suggested_command,
                "rationale": r.rationale,
                "confidence": r.confidence,
            }
        if fn_name == "explain_result":
            r = skill.explain_result(
                action.arguments.get("last_command", ""),
                action.arguments.get("last_result", ""),
            )
            return {
                "success": True, "type": "explain_result",
                "summary": r.summary,
                "safety_note": r.safety_note,
                "suggested_next_step": r.suggested_next_step,
            }
        if fn_name == "clarify_request":
            r = skill.clarify_request(action.arguments.get("user_text", ""))
            return {
                "success": True, "type": "clarify_request",
                "clarification_needed": r.clarification_needed,
                "clarification_question": r.clarification_question,
                "candidate_intents": r.candidate_intents,
            }
        if fn_name == "suggest_intent":
            r = skill.suggest_intent(action.arguments.get("user_text", ""))
            return {
                "success": True, "type": "suggest_intent",
                "intent": r.intent,
                "confidence": r.confidence,
                "explanation": r.explanation,
            }
    except RuntimeError as e:
        return {"success": False, "error": str(e), "type": action.function_name}
    except Exception as e:
        return {"success": False, "error": f"AI Assist error: {e}", "type": action.function_name}

    raise ExecutionError(f"Unhandled AI skill function: '{action.function_name}'")


def _get_tool_module(tool_name: str) -> Any:
    module_path = _TOOL_MODULES.get(tool_name)
    if module_path is None:
        raise ExecutionError(f"Unknown tool: '{tool_name}'")
    return importlib.import_module(module_path)


def _execute_action(action: ToolAction, confirmed: bool) -> dict[str, Any]:
    # Skill registry — read-only, dedicated handler, bypasses tool whitelist
    if action.tool_name == "skill":
        return _execute_skill_action(action)

    # AI Assist skill — advisory only, dedicated handler, bypasses tool whitelist
    if action.tool_name == "ai_skill":
        return _execute_ai_skill_action(action)

    if action.tool_name not in WHITELISTED_FUNCTIONS:
        raise ExecutionError(
            f"Tool '{action.tool_name}' is not whitelisted. "
            f"Allowed tools: {sorted(WHITELISTED_FUNCTIONS)}"
        )

    allowed_fns = WHITELISTED_FUNCTIONS[action.tool_name]
    if action.function_name not in allowed_fns:
        raise ExecutionError(
            f"Function '{action.function_name}' is not whitelisted for tool "
            f"'{action.tool_name}'. Allowed: {sorted(allowed_fns)}"
        )

    module = _get_tool_module(action.tool_name)
    fn = getattr(module, action.function_name, None)
    if fn is None:
        raise ExecutionError(
            f"Function '{action.function_name}' not found in tool '{action.tool_name}'"
        )

    args = copy.deepcopy(action.arguments)
    if "dry_run" in args:
        args["dry_run"] = not confirmed

    return fn(**args)


def execute(plan: ExecutionPlan, confirmed: bool) -> ExecutionResult:
    """
    Execute the plan.
    confirmed=True  → modifying actions run for real.
    confirmed=False → modifying actions run in dry-run mode (no changes).
    Read-only actions always run regardless of confirmed.
    """
    if not plan.actions:
        return ExecutionResult(
            status=OperationStatus.SKIPPED,
            message="No actions to execute.",
        )

    all_details: list[str] = []
    all_affected: list[str] = []
    all_errors: list[str] = []
    raw_results: list[dict] = []

    opened_target: str | None = None
    extracted_text_summary: str | None = None
    listed_links: list[str] = []

    for action in plan.actions:
        try:
            result = _execute_action(action, confirmed=confirmed)
            raw_results.append(result)
            if isinstance(result, dict):
                # Standard file-operation error lists
                tool_errors = result.get("errors") or []
                all_errors.extend(e for e in tool_errors if e)
                for key in ("moved", "renamed", "compressed"):
                    val = result.get(key)
                    if isinstance(val, list):
                        all_affected.extend(val)
                dest = result.get("destination")
                if isinstance(dest, str) and dest:
                    all_affected.append(dest)

                # Phone / browser specific fields
                if not result.get("success", True) and result.get("error"):
                    all_errors.append(result["error"])

                if action.function_name in ("open_url", "open_file", "open_app"):
                    opened_target = (
                        result.get("url")
                        or result.get("path")
                        or result.get("app_name")
                    )
                elif action.function_name == "browser_extract_text":
                    extracted_text_summary = result.get("text", "")
                elif action.function_name == "browser_list_links":
                    listed_links = [
                        lnk.get("url", "") for lnk in result.get("links", [])
                        if lnk.get("url")
                    ]

        except (ToolError, ExecutionError) as e:
            all_errors.append(str(e))
        except Exception as e:
            all_errors.append(f"Unexpected error in {action.function_name}: {e}")

    if all_errors and not raw_results:
        status = OperationStatus.FAILURE
    elif all_errors:
        status = OperationStatus.PARTIAL
    else:
        status = OperationStatus.SUCCESS

    message = _build_message(plan.intent, confirmed, status, is_preview=plan.dry_run)

    return ExecutionResult(
        status=status,
        message=message,
        details=all_details,
        affected_paths=all_affected,
        errors=all_errors,
        raw_results=raw_results,
        opened_target=opened_target,
        extracted_text_summary=extracted_text_summary,
        listed_links=listed_links,
    )


def _build_message(
    intent: str,
    confirmed: bool,
    status: OperationStatus,
    is_preview: bool = False,
) -> str:
    if not confirmed and is_preview:
        return f"[DRY RUN] Preview completed for '{intent}'. No changes made."
    if status == OperationStatus.SUCCESS:
        return f"Operation '{intent}' completed successfully."
    elif status == OperationStatus.PARTIAL:
        return f"Operation '{intent}' completed with some errors."
    else:
        return f"Operation '{intent}' failed."
