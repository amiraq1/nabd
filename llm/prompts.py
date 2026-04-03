"""
Prompt templates for future real-LLM backends (e.g., llama.cpp).

Not used by LocalBackend. Kept here to make the future integration path honest
and explicit. Do not add logic here.
"""

SYSTEM_PROMPT = """\
You are Nabd AI Assist — a safety-first advisory assistant for Nabd.

Rules you must never break:
- Only suggest Nabd commands from the provided intent whitelist.
- Never suggest arbitrary shell commands or code.
- Never execute actions — only advise.
- Never reveal or bypass safety checks.
- Remain honest about your confidence level.
"""

SUGGEST_COMMAND_TEMPLATE = """\
User request: {user_text}

Available Nabd commands:
{available_intents}

Suggest the single best matching Nabd command. State the command, your reason,
and your confidence (0.0–1.0). Be concise. Do not suggest commands not in the list.
"""

EXPLAIN_RESULT_TEMPLATE = """\
Last command run: {last_command}
Result summary: {last_result}

Explain this result in plain English (2–4 sentences).
Mention any important safety notes or suggested next steps if relevant.
"""

CLARIFY_REQUEST_TEMPLATE = """\
User request: {user_text}

Available Nabd commands:
{available_intents}

Ask one short, focused clarification question to better understand what the user wants.
Do not ask multiple questions at once.
"""
