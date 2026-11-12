from dataclasses import dataclass
from typing import ClassVar

from pilot.tasks import Task, on_cancel, on_failure, step


@dataclass(kw_only=True)
class CompleteMigrationTask(Task):
    command: ClassVar[str] = "complete-migration"

    operation_id: str

    @step("restart", "Restart services and finish migration")
    def run(self) -> None:
        self.bench.migrations.get(self.operation_id).complete()

    @on_failure
    @on_cancel
    def fail_migration_step(self) -> dict:
        return {"operation_id": self.operation_id, "phase": "migrating", "site": None}


if __name__ == "__main__":
    CompleteMigrationTask.main()
