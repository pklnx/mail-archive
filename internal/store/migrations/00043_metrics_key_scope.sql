-- +goose Up

-- The API-key scope metrics (#397, part 3): a key of an admin that opens
-- only GET /api/metrics. It is alone in its key, so a metrics key never
-- reads mail or starts syncs, and a read or sync key never reads the
-- metrics. The web server and the store check the same; this check is the
-- last line. Added NOT VALID and validated in a second step, so the
-- ALTER itself does not scan the table (api_keys is small either way).
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check CHECK (
    cardinality(scopes) >= 1
    AND scopes <@ ARRAY['read', 'sync', 'metrics']::text[]
    AND (NOT 'metrics' = ANY (scopes) OR scopes = ARRAY['metrics']::text[])
) NOT VALID;
ALTER TABLE api_keys VALIDATE CONSTRAINT api_keys_scopes_check;

-- +goose Down

-- The old check knows no metrics scope: those keys go, as when their owner
-- is removed. Nothing references api_keys.
DELETE FROM api_keys WHERE 'metrics' = ANY (scopes);
ALTER TABLE api_keys DROP CONSTRAINT api_keys_scopes_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scopes_check
    CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['read', 'sync']::text[]);
