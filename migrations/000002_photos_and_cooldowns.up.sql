-- Multiple profile photos. The legacy single avatar in users.custom_photo_url is kept as the
-- source of truth for every existing read path; a photo row simply describes the same file, so
-- an installation that never uploads a second photo behaves exactly as before.
CREATE TABLE IF NOT EXISTS user_photos (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Path relative to the configured photo root. Empty for a photo that lives on a remote host
    -- (an https custom_photo_url set before this table existed): there is no local file to remove.
    file_path TEXT NOT NULL,
    public_url TEXT NOT NULL,
    -- Small variant used by list cards and carousel placeholders. Empty when the photo predates
    -- this table or the thumbnail could not be written; consumers then fall back to public_url.
    thumb_url TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS user_photos_user_idx ON user_photos (user_id, sort_order, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS user_photos_primary_idx ON user_photos (user_id) WHERE is_primary = 1;

-- Backfill, idempotent: every user who already has an avatar gets exactly one primary photo row.
-- The original file is never moved or rewritten, so rolling this migration back loses no image.
INSERT INTO user_photos (id, user_id, file_path, public_url, sort_order, is_primary)
SELECT
    lower(
        hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' ||
        substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))
    ),
    users.id,
    CASE WHEN users.custom_photo_url LIKE '/profile_photo/%' THEN substr(users.custom_photo_url, 16) ELSE '' END,
    users.custom_photo_url,
    0,
    1
FROM users
WHERE users.custom_photo_url IS NOT NULL
  AND users.custom_photo_url <> ''
  AND NOT EXISTS (SELECT 1 FROM user_photos WHERE user_photos.user_id = users.id);

-- One row per (actor, target, action). `action_type` keeps the like and message timers independent
-- while sharing a single enforcement path. Timestamps are Unix milliseconds so every comparison is
-- a plain integer comparison, immune to the text-vs-datetime storage differences SQLite allows.
CREATE TABLE IF NOT EXISTS user_action_cooldowns (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action_type TEXT NOT NULL CHECK (action_type IN ('like', 'message')),
    last_action_at INTEGER NOT NULL,
    next_allowed_at INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS user_action_cooldowns_key_idx
    ON user_action_cooldowns (actor_user_id, target_user_id, action_type);
CREATE INDEX IF NOT EXISTS user_action_cooldowns_actor_idx
    ON user_action_cooldowns (actor_user_id, next_allowed_at);
