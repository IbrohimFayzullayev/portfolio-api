ALTER TABLE invitations
    DROP COLUMN IF EXISTS venue_id,
    DROP COLUMN IF EXISTS venue_name,
    DROP COLUMN IF EXISTS venue_custom;
