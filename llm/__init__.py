"""Public LLM API for backends, schemas, and backend registry helpers."""

from .backend import LLMBackend
from .backend_registry import BackendRegistry, KNOWN_BACKENDS
from .llama_cpp_backend import LlamaCppBackend
from .local_backend import LocalBackend
from .ollama_backend import OllamaBackend
from .schemas import (
    BackendStatus,
    Clarification,
    CommandSuggestion,
    IntentSuggestion,
    ResultExplanation,
)

__all__ = [
    "BackendRegistry",
    "BackendStatus",
    "Clarification",
    "CommandSuggestion",
    "IntentSuggestion",
    "KNOWN_BACKENDS",
    "LLMBackend",
    "LlamaCppBackend",
    "LocalBackend",
    "OllamaBackend",
    "ResultExplanation",
]
