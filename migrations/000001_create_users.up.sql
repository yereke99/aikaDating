CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    telegram_user_id INTEGER NOT NULL UNIQUE,
    telegram_chat_id INTEGER,
    username TEXT,
    first_name TEXT,
    last_name TEXT,
    telegram_photo_url TEXT,
    telegram_language_code TEXT,

    app_language VARCHAR(2) NOT NULL DEFAULT 'ru' CHECK (app_language IN ('ru', 'kk', 'en')),
    display_name TEXT,
    gender VARCHAR(20) CHECK (gender IS NULL OR gender IN ('male', 'female', 'other')),
    birth_date DATE,
    purpose TEXT,
    bio TEXT,
    custom_photo_url TEXT,

    latitude REAL CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
    longitude REAL CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
    location_updated_at DATETIME,

    is_profile_completed BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    is_blocked BOOLEAN NOT NULL DEFAULT FALSE,

    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS users_nearby_idx
    ON users (is_active, is_blocked, is_profile_completed)
    WHERE latitude IS NOT NULL AND longitude IS NOT NULL;

CREATE INDEX IF NOT EXISTS users_last_seen_idx ON users (last_seen_at DESC);
