-- Dropping these tables restores the single-avatar behaviour: users.custom_photo_url was never
-- rewritten by the up migration, and no photo file is deleted here.
DROP TABLE IF EXISTS user_action_cooldowns;
DROP TABLE IF EXISTS user_photos;
