-- +migrate Up
-- Optional write-path cache invalidation per source connection (default off).

ALTER TABLE connections
  ADD COLUMN IF NOT EXISTS auto_invalidate_on_write BOOLEAN NOT NULL DEFAULT FALSE;
