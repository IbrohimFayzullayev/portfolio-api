-- A sprint is judged by one question at the end: did the result happen?
--
-- There is no daily quota and no streak (see internal/growth): those measure
-- attendance, and attendance is not learning. What is recorded instead is the
-- honest answer when a sprint is closed. NULL means "closed, not answered yet"
-- (or still open).
ALTER TABLE growth_sprints
    ADD COLUMN outcome TEXT CHECK (outcome IN ('done', 'partial', 'missed'));
