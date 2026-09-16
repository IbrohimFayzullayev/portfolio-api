-- The guest's name, as the invitation site now collects it.
--
-- NOT NULL DEFAULT '' rather than a nullable column: every other text field on
-- this table follows that rule, and it means the rows written before today read
-- as "no name given" instead of NULL, which the API and the bot would each
-- have to special-case.
--
-- Nothing backfills. The existing rows genuinely do not have a name.

ALTER TABLE invitations ADD COLUMN guest_name TEXT NOT NULL DEFAULT '';
