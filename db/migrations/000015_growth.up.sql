-- Personal growth log: English (IELTS) practice and books, written from Telegram.
--
-- Same idea as finance_entries: one short message writes one row, and every
-- report is a query over the rows. Nothing here is a counter that has to be
-- kept in sync — a streak, a sprint's result, a book's total time are all
-- computed when they are asked for, so deleting a wrong row fixes everything.
--
-- Days are stored as the Asia/Tashkent calendar day the work belongs to, not
-- as a timestamp: "kecha 40 daq" written at 08:00 belongs to yesterday.

-- A sprint is a short stretch (Mon–Sat) with one result written as one
-- sentence. Its numbers are read from the logs between starts_on and ends_on,
-- so there is no sprint_id to keep right on every row.
CREATE TABLE growth_sprints (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    goal        TEXT NOT NULL CHECK (length(goal) BETWEEN 3 AND 300),
    starts_on   DATE NOT NULL,
    ends_on     DATE NOT NULL CHECK (ends_on >= starts_on),
    result_note TEXT NOT NULL DEFAULT '',
    closed_at   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At most one open sprint. Opening a second one is a decision to close the
-- first, which /yakun makes explicit.
CREATE UNIQUE INDEX idx_growth_sprints_one_open
    ON growth_sprints ((closed_at IS NULL)) WHERE closed_at IS NULL;

CREATE TABLE growth_logs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    day        DATE NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN
                   ('writing', 'speaking', 'listening', 'reading',
                    'vocab', 'grammar', 'mock', 'other')),
    minutes    INT  NOT NULL CHECK (minutes BETWEEN 1 AND 600),
    note       TEXT NOT NULL DEFAULT '',
    -- 'command' (/qildim) or 'voice' (a voice message counted as speaking).
    source     TEXT NOT NULL DEFAULT 'command' CHECK (source IN ('command', 'voice')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_growth_logs_day ON growth_logs (day);

-- Voice messages. Only Telegram's file_id is kept: the bot can send the same
-- audio back with it at any time, so storing the file would be a copy of
-- something Telegram already holds.
CREATE TABLE growth_recordings (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    day              DATE NOT NULL,
    telegram_file_id TEXT NOT NULL,
    file_unique_id   TEXT NOT NULL UNIQUE,
    duration_sec     INT  NOT NULL CHECK (duration_sec >= 0),
    caption          TEXT NOT NULL DEFAULT '',
    log_id           UUID REFERENCES growth_logs (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_growth_recordings_created ON growth_recordings (created_at);

-- Band scores: mock tests, tutor estimates, the real exam.
CREATE TABLE growth_scores (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    taken_on   DATE NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'mock' CHECK (kind IN ('mock', 'official', 'tutor')),
    overall    NUMERIC(2,1) NOT NULL CHECK (overall BETWEEN 0 AND 9 AND overall * 2 = floor(overall * 2)),
    listening  NUMERIC(2,1) CHECK (listening BETWEEN 0 AND 9 AND listening * 2 = floor(listening * 2)),
    reading    NUMERIC(2,1) CHECK (reading   BETWEEN 0 AND 9 AND reading   * 2 = floor(reading   * 2)),
    writing    NUMERIC(2,1) CHECK (writing   BETWEEN 0 AND 9 AND writing   * 2 = floor(writing   * 2)),
    speaking   NUMERIC(2,1) CHECK (speaking  BETWEEN 0 AND 9 AND speaking  * 2 = floor(speaking  * 2)),
    note       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_growth_scores_taken ON growth_scores (taken_on DESC, created_at DESC);

-- Books read for pleasure (novels, mostly in Uzbek). Kept apart from
-- growth_logs on purpose: reading a novel is not English practice and must
-- not quietly satisfy the daily English minimum.
CREATE TABLE books (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    author      TEXT NOT NULL DEFAULT '',
    total_pages INT  CHECK (total_pages > 0),
    status      TEXT NOT NULL DEFAULT 'reading' CHECK (status IN ('reading', 'finished', 'dropped')),
    started_on  DATE NOT NULL,
    finished_on DATE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The same title cannot be "being read" twice; a reread after finishing is a
-- new row, which is what a reread is.
CREATE UNIQUE INDEX idx_books_reading_title ON books (lower(title)) WHERE status = 'reading';

CREATE TABLE book_sessions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id      UUID NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    day          DATE NOT NULL,
    minutes      INT  NOT NULL CHECK (minutes BETWEEN 1 AND 720),
    -- The page reached at the end of the session, when it was given.
    page_reached INT  CHECK (page_reached > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_book_sessions_book ON book_sessions (book_id, day);
CREATE INDEX idx_book_sessions_day  ON book_sessions (day);
