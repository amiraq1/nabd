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

    for action in plan.actions:
        try:
            result = _execute_action(action, confirmed=confirmed)
            raw_results.append(result)
            if isinstance(result, dict):
                tool_errors = result.get("errors") or []
                all_errors.extend(e for e in tool_errors if e)
                for key in ("moved", "renamed", "compressed"):
                    val = result.get(key)
                    if isinstance(val, list):
                        all_affected.extend(val)
                dest = result.get("destination")
                if isinstance(dest, str) and dest:
                    all_affected.append(dest)
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
