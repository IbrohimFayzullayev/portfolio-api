-- What AI agents changed, one row per tool call.
--
-- The MCP server (cmd/portfolio-mcp) reports every write it makes through
-- the API - posts, projects, links, read marks - with the values before and
-- after, so a change can be seen, explained and put back by hand. A failed
-- write is recorded too: an agent that keeps trying the wrong thing is worth
-- knowing about.
--
-- `actor` names the agent ('mcp' today; the Telegram /ai command later), so
-- one table serves every agent and the dashboard can show them together.
-- Rows are never updated; this is a log.

CREATE TABLE agent_changes (
    id          BIGSERIAL PRIMARY KEY,
    actor       TEXT        NOT NULL DEFAULT 'mcp',
    tool        TEXT        NOT NULL,
    -- post | project | message
    target_type TEXT        NOT NULL,
    -- [{"id": "...", "label": "uz/vps-deploy"}]: link_translations touches two
    targets     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    -- {"title": {"from": "...", "to": "..."}}: full values, so nothing is lost
    changes     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    ok          BOOLEAN     NOT NULL,
    error       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_changes_created ON agent_changes (created_at DESC);
