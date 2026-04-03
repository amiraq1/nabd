"""
Abstract LLM backend interface.
All implementations must be safe, advisory-only, and never execute tool actions.
"""
from __future__ import annotations
from abc import ABC, abstractmethod

from llm.schemas import CommandSuggestion, Clarification, IntentSuggestion, ResultExplanation


class LLMBackend(ABC):
    """
    Common interface for all Nabd LLM backends.

    Implementations may be deterministic (LocalBackend), stub, or future
    llama.cpp adapters. None of them may call tool functions or execute actions.
    """

    @abstractmethod
    def is_available(self) -> bool:
        """Return True if this backend can currently handle requests."""
        ...

    @abstractmethod
    def suggest_command(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> CommandSuggestion:
        """Suggest the best matching Nabd command for the user's request."""
        ...

    @abstractmethod
    def explain_result(
        self,
        last_command: str,
        last_result: str,
    ) -> ResultExplanation:
        """Produce a plain-English explanation of the last operation's result."""
        ...

    @abstractmethod
    def clarify_request(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> Clarification:
        """Ask one focused clarification question for an ambiguous request."""
        ...

    @abstractmethod
    def suggest_intent(
        self,
        user_text: str,
        allowed_intents: list[str],
    ) -> IntentSuggestion:
        """
        Return the single best-matching intent name from allowed_intents.
        Must never return an intent that is not in allowed_intents.
        """
        ...
