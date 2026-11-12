from dataclasses import dataclass


@dataclass
class PostgresConfig:
    host: str = "localhost"
    # Avoids clashing with a stock PostgreSQL install on the well-known 5432.
    port: int = 5450
    root_password: str = ""
    admin_user: str = "postgres"
    existing: bool = False
