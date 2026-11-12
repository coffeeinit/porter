import re
from dataclasses import dataclass, field

from pilot.exceptions import ConfigError

_QUEUE_NAME_PATTERN = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


@dataclass
class WorkerGroup:
    """One worker group: spawn ``count`` workers listening to ``queues``."""

    queues: list[str]
    count: int


@dataclass
class WorkerConfig:
    groups: list[WorkerGroup] = field(
        default_factory=lambda: [
            WorkerGroup(queues=["default", "short", "long"], count=1),
        ]
    )

    @property
    def queues(self) -> list[str]:
        """Every queue across the groups, deduped, order preserved."""
        return list(dict.fromkeys(queue for group in self.groups for queue in group.queues))

    @property
    def count(self) -> int:
        """Workers across every group."""
        return sum(group.count for group in self.groups)

    def collapse(self) -> None:
        """Fold the groups into one pool: the union of the queues, the total count."""
        self.groups = [WorkerGroup(queues=self.queues, count=self.count)]

    @classmethod
    def from_dict(cls, data: list) -> "WorkerConfig":
        # [[workers]] array-of-tables: each group lists queues and a count.
        if not isinstance(data, list) or not data:
            return cls()
        groups = [
            WorkerGroup(
                queues=entry.get("queues", [entry.get("queue", "default")]),
                count=entry.get("count", 1),
            )
            for entry in data
        ]
        return cls(groups=groups)

    def validate(self) -> None:
        if not self.groups:
            raise ConfigError("workers.groups must contain at least one worker group.")
        for i, group in enumerate(self.groups):
            prefix = f"workers[{i}]"
            if not isinstance(group.queues, list) or not group.queues:
                raise ConfigError(f"{prefix}.queues must be a non-empty list.")
            for queue in group.queues:
                # A queue name reaches a systemd ExecStart and a supervisor stanza, where
                # a newline would start a directive of the attacker's choosing.
                if not isinstance(queue, str) or not _QUEUE_NAME_PATTERN.match(queue):
                    raise ConfigError(
                        f"{prefix}.queues entries must be 1-64 characters of letters, "
                        f"numbers, '-' or '_' (got {queue!r})."
                    )
            if not isinstance(group.count, int) or group.count < 1:
                raise ConfigError(f"{prefix}.count must be a positive integer, got '{group.count}'.")
