from __future__ import annotations

from dataclasses import dataclass
from typing import Literal


@dataclass(frozen=True)
class RevisionPin:
    """A fixed app revision target: tag or commit, never a branch."""

    kind: Literal["tag", "commit"]
    ref: str

    @classmethod
    def from_marketplace_release(cls, release: dict) -> "RevisionPin | None":
        """Build a commit pin from a registry release, or None when it advertises no commit."""
        commit = release.get("commit")
        return cls(kind="commit", ref=commit) if commit else None
