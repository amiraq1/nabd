import copy
from typing import Any

from agent.models import ExecutionPlan, ExecutionResult, OperationStatus, ToolAction
from core.exceptions import ExecutionError, ToolError


WHITELISTED_FUNCTIONS: dict[str, set[str]] = {
    "system":     {"run_doctor"},
    "storage":    {"get_storage_report", "list_large_files"},
    "files":      {"organize_folder_by_type", "safe_rename_files", "safe_move_files",
                   "show_files", "list_media"},
    "media":      {"convert_video_to_mp3", "compress_images"},
    "backup":     {"backup_folder"},
    "duplicates": {"find_duplicates"},
    "phone":      {"open_app", "open_file", "open_url",
                   "get_battery_status", "get_network_status"},
    "browser":    {"browser_search", "browser_extract_text", "browser_list_links"},
}


def _get_tool_module(tool_name: str) -> Any:
    if tool_name == "system":
        import tools.system as mod
        return mod
    elif tool_name == "storage":
        import tools.storage as mod
        return mod
    elif tool_name == "files":
        import tools.files as mod
        return mod
    elif tool_name == "media":
        import tools.media as mod
        return mod
    elif tool_name == "backup":
        import tools.backup as mod
        return mod
    elif tool_name == "duplicates":
        import tools.duplicates as mod
        return mod
    elif tool_name == "phone":
        import tools.phone as mod
        return mod
    elif tool_name == "browser":
        import tools.browser as mod
        return mod
    else:
        raise ExecutionError(f"Unknown tool: '{tool_name}'")


def _execute_action(action: ToolAction, confirmed: bool) -> dict[str, Any]:
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

    message = _build_message(plan.intent, confirmed, status)

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


def _build_message(intent: str, confirmed: bool, status: OperationStatus) -> str:
    if not confirmed:
        return f"[DRY RUN] Preview completed for '{intent}'. No changes made."
    if status == OperationStatus.SUCCESS:
        return f"Operation '{intent}' completed successfully."
    elif status == OperationStatus.PARTIAL:
        return f"Operation '{intent}' completed with some errors."
    else:
        return f"Operation '{intent}' failed."
