-- 0035_engine_defaults: per-engine superuser + default healthcheck probe,
-- previously hardcoded in marketplace.defaultServiceUser. The DB is the
-- source of truth; the Go map remains as fallback only for when the DB is
-- unreachable. service_templates rows (0034) join to this table by engine
-- name for superuser/healthcheck resolution in ResolveTemplate.
CREATE TABLE IF NOT EXISTS engine_defaults (
  engine      TEXT PRIMARY KEY,
  superuser   TEXT NOT NULL,
  hc_type     TEXT NOT NULL DEFAULT 'tcp',
  hc_port     INT NOT NULL,
  hc_interval INT NOT NULL DEFAULT 10
);
-- Seeds mirror the built-in catalog (marketplace.ServiceTemplates):
-- minio probes 9000/http (console 9001 is publish-only, not probed).
INSERT INTO engine_defaults (engine, superuser, hc_type, hc_port, hc_interval) VALUES
  ('postgres', 'postgres', 'tcp', 5432, 10),
  ('mysql',    'root',     'tcp', 3306, 10),
  ('mariadb',  'root',     'tcp', 3306, 10),
  ('mongo',    'root',     'tcp', 27017, 10),
  ('redis',    'default',  'tcp', 6379, 10),
  ('minio',    'porter',   'http', 9000, 10)
ON CONFLICT (engine) DO UPDATE SET superuser = EXCLUDED.superuser,
  hc_type = EXCLUDED.hc_type, hc_port = EXCLUDED.hc_port,
  hc_interval = EXCLUDED.hc_interval;
