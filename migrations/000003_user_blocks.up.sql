-- Person-to-person blocking. This is separate from users.is_blocked, which is the moderation flag
-- an administrator sets on a whole account: one row here only hides two people from each other.
--
-- A block is stored once, in the direction it was made, so the list of "people I blocked" is a
-- plain lookup by blocker_user_id. Every visibility rule reads it in both directions, so the
-- person who was blocked also stops seeing the person who blocked them without a mirrored row.
CREATE TABLE IF NOT EXISTS user_blocks (
    id TEXT PRIMARY KEY,
    blocker_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (blocker_user_id <> blocked_user_id)
);

-- Blocking twice is the same as blocking once, which lets the endpoint be safely retried.
CREATE UNIQUE INDEX IF NOT EXISTS user_blocks_pair_idx
    ON user_blocks (blocker_user_id, blocked_user_id);
-- The reverse lookup, for hiding a viewer from the person who blocked them.
CREATE INDEX IF NOT EXISTS user_blocks_blocked_idx
    ON user_blocks (blocked_user_id);
