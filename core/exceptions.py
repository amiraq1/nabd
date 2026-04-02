class NabdError(Exception):
    pass


class SafetyError(NabdError):
    pass


class PathTraversalError(SafetyError):
    pass


class PathNotAllowedError(SafetyError):
    pass


class ConfirmationRequiredError(NabdError):
    pass


class ValidationError(NabdError):
    pass


class ExecutionError(NabdError):
    pass


class ConfigError(NabdError):
    pass


class ToolError(NabdError):
    pass


class UnknownIntentError(NabdError):
    pass
