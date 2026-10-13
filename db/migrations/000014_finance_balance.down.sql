DROP TABLE IF EXISTS finance_checkpoints;

DELETE FROM finance_entries WHERE kind = 'income';
ALTER TABLE finance_entries DROP CONSTRAINT finance_entries_kind_check;
ALTER TABLE finance_entries ADD CONSTRAINT finance_entries_kind_check CHECK (kind IN
    ('expense', 'gift', 'lent', 'repaid_in', 'borrowed', 'repaid_out'));
