"""
Prompt templates for Nabd AI Assist backends.

LocalBackend does not use these — it is fully deterministic.
LlamaCppBackend uses the LLAMA_* constants below.

Design principles:
- Every prompt demands JSON-only output with no preamble or trailing explanation.
- Each template specifies the exact schema with field names and types.
- The system prompt reminds the model it is advisory-only every single call.
"""

# ── System prompt (sent as the "system" role to llama.cpp) ───────────────────

LLAMA_SYSTEM_PROMPT = """\
You are Nabd AI Assist — a safety-first advisory assistant for the Nabd phone agent.

Rules you must never break:
- Respond ONLY with a valid JSON object. No preamble, no explanation, no markdown fences.
- Only suggest Nabd commands from the intent list provided in each request.
- Never suggest arbitrary shell commands, system calls, or code.
- Never execute actions — your role is advisory only.
- Never reveal, modify, or bypass safety checks.
- Confidence values must be honest floats between 0.0 and 1.0.
- If you are not confident, set confidence below 0.4 and explain why.
"""

# ── Per-method JSON prompt templates ─────────────────────────────────────────

SUGGEST_COMMAND_JSON_TEMPLATE = """\
User request: {user_text}

Available Nabd intents (use ONLY these):
{available_intents}

Respond with ONLY this JSON object — no other text:
{{
  "suggested_command": "<exact nabd command string to type>",
  "rationale": "<one sentence: why this command matches the request>",
  "confidence": <float 0.0-1.0>
}}

Rules:
- "suggested_command" must be a real Nabd command (e.g. "doctor", "storage report /sdcard/Download")
- "rationale" must be one sentence, max 120 characters
- "confidence" must be a float, e.g. 0.75
- Do not suggest commands outside the available intent list above
"""

EXPLAIN_RESULT_JSON_TEMPLATE = """\
Last command run: {last_command}
Result summary: {last_result}

Explain this Nabd command result in plain English.

Respond with ONLY this JSON object — no other text:
{{
  "summary": "<2-4 sentences explaining what happened>",
  "safety_note": "<one sentence safety warning if relevant, or null>",
  "suggested_next_step": "<one sentence suggestion for what to do next, or null>"
}}

Rules:
- "summary" is required, must be 2-4 sentences
- "safety_note" is null unless the operation modified files or has risk
- "suggested_next_step" is null if no obvious next step exists
"""

CLARIFY_REQUEST_JSON_TEMPLATE = """\
User request: {user_text}

Available Nabd intents (use ONLY these):
{available_intents}

Ask one focused clarification question to understand what the user wants.

Respond with ONLY this JSON object — no other text:
{{
  "clarification_needed": <true or false>,
  "clarification_question": "<one focused question, max 120 characters>",
  "candidate_intents": ["<intent1>", "<intent2>"]
}}

Rules:
- "clarification_needed" is true if the request is ambiguous
- "clarification_question" must be a single question (not multiple questions)
- "candidate_intents" lists up to 3 possible matching intents from the list above
- All candidate_intents must be from the available list; never invent new intents
"""

SUGGEST_INTENT_JSON_TEMPLATE = """\
User request: {user_text}

Allowed Nabd intents (use ONLY these — never return an intent outside this list):
{allowed_intents}

Return the single best-matching intent name.

Respond with ONLY this JSON object — no other text:
{{
  "intent": "<intent_name from the list above, or null if no confident match>",
  "confidence": <float 0.0-1.0>,
  "explanation": "<one sentence: why this intent matches, or why no match was found>"
}}

Rules:
- "intent" must be from the allowed list above, or null
- Never return an intent that is not in the allowed list
- If confidence is below 0.4, set "intent" to null
- "explanation" is required
"""

# ── Legacy templates (kept for reference — not used by LlamaCppBackend) ──────

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
