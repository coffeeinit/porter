import sys
from dataclasses import dataclass
from typing import ClassVar

from pilot.tasks import Task, on_cancel, on_failure


@dataclass(kw_only=True)
class RestartServicesTask(Task):
    """Chain link: restart services to finish a restore, then mark the operation reverted."""

    command: ClassVar[str] = "restart-services"

    operation_id: str

    def run(self) -> None:
        operation = self.bench.migrations.get(self.operation_id)
        try:
            operation.restart(on_step=self.step)
        except Exception:
            self.step_failed()
            sys.exit(1)
        operation.enqueue_next(handoff_from=operation.chain[-1]["task_id"])

    @on_failure
    @on_cancel
    def fail_migration_step(self) -> dict:
        return {"operation_id": self.operation_id, "phase": "restarting", "site": None}


if __name__ == "__main__":
    RestartServicesTask.main()
