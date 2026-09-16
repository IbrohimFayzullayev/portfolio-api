-- The exact venue, on top of the place category.
--
-- place_* already says WHAT KIND of place it is ("park", "cafe"), chosen from
-- the site's list. These three say WHICH ONE, and whether the visitor picked it
-- from a list or typed it themselves:
--
--   venue_id      the list entry's id, or 'custom' when they typed it
--   venue_name    the venue as it should be read back
--   venue_custom  true when they typed it, false when they chose it
--
-- venue_custom is stored rather than derived from venue_id = 'custom' because
-- the two can drift: the site may rename that sentinel, and a boolean that
-- means what it says survives that.
--
-- VARCHAR rather than the TEXT used elsewhere in this table, matching the
-- lengths the invitation site was specified with. The handler clamps to the
-- same numbers before the insert, so the length limit here is a second belt
-- and never the thing that rejects a submission — losing a whole invitation
-- over one long field would be the wrong trade.

ALTER TABLE invitations
    ADD COLUMN venue_id     VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN venue_name   VARCHAR(80) NOT NULL DEFAULT '',
    ADD COLUMN venue_custom BOOLEAN     NOT NULL DEFAULT false;
