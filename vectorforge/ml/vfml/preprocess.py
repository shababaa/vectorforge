"""Text preprocessing shared by the service, training and evaluation.

Queries and documents must be preprocessed identically, otherwise the model
sees systematically different inputs at index time and at query time. The
rules are deliberately light: subword tokenizers already ignore whitespace
layout, and code indentation must not be destroyed in stored text.
"""

from __future__ import annotations

import unicodedata

MAX_TEXT_CHARS = 8192


class TextError(ValueError):
    """Raised for text that cannot be embedded."""


def normalize_text(text: str) -> str:
    """Return NFC-normalized text without control characters (except tab/newline).

    Line endings become ``\\n``. Leading and trailing whitespace is removed.
    Interior whitespace, indentation and punctuation are preserved.
    """
    if not isinstance(text, str):
        raise TextError("text must be a string")
    text = unicodedata.normalize("NFC", text.replace("\r\n", "\n").replace("\r", "\n"))
    cleaned = "".join(ch for ch in text if ch in "\n\t" or unicodedata.category(ch) != "Cc")
    return cleaned.strip()


def validate_text(text: str) -> str:
    """Normalize and validate one input, raising TextError when it is unusable."""
    normalized = normalize_text(text)
    if not normalized:
        raise TextError("text is empty after normalization")
    if len(normalized) > MAX_TEXT_CHARS:
        raise TextError(f"text has {len(normalized)} characters; maximum is {MAX_TEXT_CHARS}")
    return normalized
