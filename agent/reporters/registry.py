from typing import Any, Callable

RawDetailRenderer = Callable[[list[str], dict[str, Any], bool], None]
_RAW_DETAIL_RENDERERS: dict[str, RawDetailRenderer] = {}


def register_raw_detail(*intents: str) -> Callable[[RawDetailRenderer], RawDetailRenderer]:
    def decorator(renderer: RawDetailRenderer) -> RawDetailRenderer:
        for intent in intents:
            _RAW_DETAIL_RENDERERS[intent] = renderer
        return renderer

    return decorator


def get_raw_detail_renderer(intent: str) -> RawDetailRenderer | None:
    return _RAW_DETAIL_RENDERERS.get(intent)
