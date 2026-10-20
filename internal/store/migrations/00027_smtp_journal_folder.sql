-- +goose Up

-- One folder per smtp account (#112): the journal folder is renamed from
-- INBOX to JOURNAL, and fills write into it too instead of into folders of
-- their own ("<prefix>/<IMAP folder>"). Journaled and imported messages
-- stay apart by the envelope: a location without one came from a fill.
-- Goose runs this file in one transaction, so location_retain_until never
-- sees a renamed folder without its renamed rule.

-- The application never creates a folder JOURNAL in an smtp account (only
-- INBOX, and fill folders always contain a "/"); this is a safety check.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM folders f JOIN accounts a ON a.id = f.account_id
               WHERE a.kind = 'smtp' AND f.name = 'JOURNAL') THEN
        RAISE EXCEPTION 'an smtp account already has a folder JOURNAL; rename or remove it before upgrading';
    END IF;
END $$;
-- +goose StatementEnd

SELECT audit_log_ensure_partitions(1);

-- A rule on the folder JOURNAL of an smtp account matches no folder today,
-- so its mail is kept for ever. After the rename it would match and could
-- shorten retention; deleting it only lengthens retention.
WITH removed AS (
    DELETE FROM retention_rules r
    USING accounts a
    WHERE a.id = r.account_id AND a.kind = 'smtp' AND r.folder = 'JOURNAL'
    RETURNING r.id, r.account_id, r.years, a.name
)
INSERT INTO audit_log (actor, action, target_type, target_id, target_name, details)
SELECT 'cli', 'retention.rule_remove', 'account', account_id::text, name,
       jsonb_build_object('by', 'migration 00027', 'rule_id', id, 'folder', 'JOURNAL', 'years', years,
                          'reason', 'matched no folder')
FROM removed;

-- Rules first, then the folders, removed accounts included. Folder IDs
-- stay, so locations, envelopes, UIDs and the retention ends do not change.
WITH renamed AS (
    UPDATE retention_rules r SET folder = 'JOURNAL'
    FROM accounts a
    WHERE a.id = r.account_id AND a.kind = 'smtp' AND r.folder = 'INBOX'
    RETURNING r.id, r.account_id
), folders_renamed AS (
    UPDATE folders f SET name = 'JOURNAL'
    FROM accounts a
    WHERE a.id = f.account_id AND a.kind = 'smtp' AND f.name = 'INBOX'
    RETURNING f.account_id, a.name
)
INSERT INTO audit_log (actor, action, target_type, target_id, target_name, details)
SELECT 'cli', 'account.change', 'account', fr.account_id::text, fr.name,
       jsonb_build_object('by', 'migration 00027', 'fields', jsonb_build_array('folder'),
                          'folder', jsonb_build_object('from', 'INBOX', 'to', 'JOURNAL'),
                          'retention_rules', coalesce((SELECT jsonb_agg(rn.id ORDER BY rn.id) FROM renamed rn
                                                       WHERE rn.account_id = fr.account_id), '[]'::jsonb))
FROM folders_renamed fr;

-- The fill a location came from, for the message view.
ALTER TABLE message_locations ADD COLUMN fill_id BIGINT REFERENCES smtp_fills (id) ON DELETE SET NULL;
-- Keeps ON DELETE SET NULL from scanning every location when a fill goes
-- (only with its account).
CREATE INDEX message_locations_fill_idx ON message_locations (fill_id) WHERE fill_id IS NOT NULL;
-- Every location in a folder an earlier fill wrote came from that fill:
-- prefixes were unique per account.
UPDATE message_locations l SET fill_id = ff.fill_id
FROM (SELECT DISTINCT ON (folder_id) folder_id, fill_id FROM smtp_fill_folders ORDER BY folder_id, fill_id) ff
WHERE l.folder_id = ff.folder_id;

-- New fills have no prefix: they write into JOURNAL. The CHECK of 00026
-- passes for NULL; earlier fills keep theirs.
ALTER TABLE smtp_fills ALTER COLUMN folder_prefix DROP NOT NULL;

-- +goose Down

-- The old code treats INBOX of an smtp account as received over SMTP only,
-- and has no rule on JOURNAL to rename back to. Rules deleted by Up are not
-- restored.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM retention_rules r JOIN accounts a ON a.id = r.account_id
               WHERE a.kind = 'smtp' AND r.folder = 'INBOX') THEN
        RAISE EXCEPTION 'an smtp account has a retention rule on the folder INBOX; remove it before rolling back';
    END IF;
    IF EXISTS (SELECT 1 FROM message_locations l
               JOIN folders f ON f.id = l.folder_id
               JOIN accounts a ON a.id = f.account_id
               LEFT JOIN envelopes e ON e.location_id = l.id
               WHERE a.kind = 'smtp' AND f.name = 'JOURNAL' AND e.location_id IS NULL) THEN
        RAISE EXCEPTION 'JOURNAL of an smtp account holds mail imported by a fill; it would be taken for mail received over SMTP';
    END IF;
    IF EXISTS (SELECT 1 FROM smtp_fills WHERE folder_prefix IS NULL AND status = 'running') THEN
        RAISE EXCEPTION 'a fill into JOURNAL is running; cancel it before rolling back';
    END IF;
    -- Ended fills into JOURNAL get the prefix fill-<id> below; it must not
    -- be another fill's prefix in the same account.
    IF EXISTS (SELECT 1 FROM smtp_fills sf JOIN smtp_fills other ON other.account_id = sf.account_id
               WHERE sf.folder_prefix IS NULL AND other.folder_prefix = 'fill-' || sf.id) THEN
        RAISE EXCEPTION 'another fill already uses the prefix fill-<id> that a fill into JOURNAL would get; rolling back is not possible';
    END IF;
END $$;
-- +goose StatementEnd

SELECT audit_log_ensure_partitions(1);

-- Ended fills into JOURNAL imported nothing that is still stored (checked
-- above). They get a prefix of their own, and a resume starts their
-- folders again there.
DELETE FROM smtp_fill_folders ff USING smtp_fills sf
WHERE sf.id = ff.fill_id AND sf.folder_prefix IS NULL;
WITH prefixed AS (
    UPDATE smtp_fills SET folder_prefix = 'fill-' || id WHERE folder_prefix IS NULL
    RETURNING id, account_id, folder_prefix
)
INSERT INTO audit_log (actor, action, target_type, target_id, target_name, details)
SELECT 'cli', 'account.change', 'account', p.account_id::text, a.name,
       jsonb_build_object('by', 'migration 00027 down', 'fields', jsonb_build_array('fill_prefix'),
                          'fill', p.id, 'prefix', p.folder_prefix)
FROM prefixed p JOIN accounts a ON a.id = p.account_id;
ALTER TABLE smtp_fills ALTER COLUMN folder_prefix SET NOT NULL;

DROP INDEX message_locations_fill_idx;
ALTER TABLE message_locations DROP COLUMN fill_id;

WITH renamed AS (
    UPDATE retention_rules r SET folder = 'INBOX'
    FROM accounts a
    WHERE a.id = r.account_id AND a.kind = 'smtp' AND r.folder = 'JOURNAL'
    RETURNING r.id, r.account_id
), folders_renamed AS (
    UPDATE folders f SET name = 'INBOX'
    FROM accounts a
    WHERE a.id = f.account_id AND a.kind = 'smtp' AND f.name = 'JOURNAL'
    RETURNING f.account_id, a.name
)
INSERT INTO audit_log (actor, action, target_type, target_id, target_name, details)
SELECT 'cli', 'account.change', 'account', fr.account_id::text, fr.name,
       jsonb_build_object('by', 'migration 00027 down', 'fields', jsonb_build_array('folder'),
                          'folder', jsonb_build_object('from', 'JOURNAL', 'to', 'INBOX'),
                          'retention_rules', coalesce((SELECT jsonb_agg(rn.id ORDER BY rn.id) FROM renamed rn
                                                       WHERE rn.account_id = fr.account_id), '[]'::jsonb))
FROM folders_renamed fr;
