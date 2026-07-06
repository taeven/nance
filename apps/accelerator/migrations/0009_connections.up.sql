-- +migrate Up
-- Multi-connection: named source Mongo URIs per org; tokens bind to a connection.
-- Keep statements free of PL/pgSQL (DO $$) — control plane splits SQL on ';' only.

CREATE TABLE IF NOT EXISTS connections (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    uri_ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    dek_version TEXT NOT NULL DEFAULT 'v1',
    last_validated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_connections_tenant ON connections(tenant_id);

ALTER TABLE tokens ADD COLUMN IF NOT EXISTS connection_id TEXT;

INSERT INTO connections (id, tenant_id, name, uri_ciphertext, nonce, dek_version, last_validated_at, created_at, updated_at)
SELECT
    'conn_default_' || tenant_id,
    tenant_id,
    'default',
    uri_ciphertext,
    nonce,
    dek_version,
    last_validated_at,
    created_at,
    updated_at
FROM tenant_backends
ON CONFLICT (id) DO NOTHING;

UPDATE tokens t
SET connection_id = 'conn_default_' || t.tenant_id
WHERE t.connection_id IS NULL
  AND EXISTS (SELECT 1 FROM connections c WHERE c.id = 'conn_default_' || t.tenant_id);

ALTER TABLE tokens DROP CONSTRAINT IF EXISTS tokens_connection_id_fkey;

ALTER TABLE tokens
  ADD CONSTRAINT tokens_connection_id_fkey
  FOREIGN KEY (connection_id) REFERENCES connections(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_tokens_connection ON tokens(connection_id);
