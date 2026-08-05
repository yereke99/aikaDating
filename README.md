# AikaBot

AikaBot is a small Telegram bot and Telegram Mini App for discovering nearby people, viewing short profiles, and sending a like or a short personal message. It runs as one Go process and stores application data in three SQLite tables: `users`, `user_photos`, and `user_action_cooldowns`.

Like and message *content* is delivered immediately through the Telegram Bot API and is never written to SQLite. Only the per-target cooldown timestamps are stored.

## Architecture

- Go HTTP API and Telegram long-polling bot in one process
- File-backed SQLite database in WAL mode, with a 5-second busy timeout
- React + TypeScript + Vite Mini App, served by Go in production
- Telegram `initData` authentication on every protected API request
- In-memory, per-sender one-minute like rate limiter
- Database-authoritative, per-target 30-minute cooldown for likes and for messages, tracked separately
- Up to three profile photos per user, stored per-owner on disk and mirrored into the profile avatar
- Two-second nearby refresh with entity-tag revalidation, so an unchanged neighbourhood costs a 304

SQLite `INTEGER` is a signed 64-bit value, so both `telegram_user_id` and `telegram_chat_id` retain Telegram's full numeric IDs. Internal profile IDs are random UUIDv4 strings.

## Local development

Requirements: Go 1.25+, Node.js 22.12+ (Node.js 22 LTS recommended), and npm.

With `nvm`, select the repository's Node.js version before installing frontend dependencies:

```bash
nvm install
nvm use
```

```bash
cp .env.example .env
```

Set a development bot token and username in `.env`. For a normal browser session outside Telegram, also set:

```dotenv
APP_ENV=development
LOCAL_DEV=true
DEV_TELEGRAM_USER_ID=123456789
MINI_APP_URL=http://localhost:5173
MINI_APP_ORIGIN=http://localhost:5173
WEB_DIR=./web/dist
```

`LOCAL_DEV` uses only the server-configured `DEV_TELEGRAM_USER_ID`; the browser cannot choose a Telegram user ID. The application refuses to start with `LOCAL_DEV=true` in production.

Start the API and bot:

```bash
make run
```

`make run` installs the pinned Air development tool into `.tools/bin`, builds the React Mini App and Go binary, loads `.env`, and automatically rebuilds/restarts after changes to Go, React, styles, JSON, HTML, or SQL files.

Start Vite in another terminal:

```bash
cd web
npm install
npm run dev
```

Open `http://localhost:5173`. Vite proxies `/api` to `http://localhost:8080`.

To test inside Telegram, use an HTTPS tunnel or a development HTTPS domain, configure it in BotFather, set `LOCAL_DEV=false`, and open the Mini App from Telegram so real `initData` is present.

## Database and migrations

The application applies every `migrations/*.up.sql` file in filename order at startup:

- [`000001_create_users.up.sql`](migrations/000001_create_users.up.sql) — the `users` table and its indexes.
- [`000002_photos_and_cooldowns.up.sql`](migrations/000002_photos_and_cooldowns.up.sql) — `user_photos` (gallery, one primary photo per user) and `user_action_cooldowns` (one row per actor/target/action). It also backfills one photo row for every user who already had an avatar, without moving or rewriting any file.

Each file is written to be safe to re-run — `IF NOT EXISTS` for schema, `NOT EXISTS` guards for data — so no migration-history table is needed. `000002_photos_and_cooldowns.down.sql` drops only the two new tables; `users.custom_photo_url` is left intact by the up migration, so a rollback restores the original single-avatar behaviour and loses no image.

There is still no likes, messages, sessions, or admin table.

The default local file is `./data/aikabot.db`. SQLite WAL sidecar files can exist while the process is running; they are part of SQLite's storage mechanism, not additional application databases.

For a consistent backup, stop the app or use SQLite's online backup command:

```bash
sqlite3 data/aikabot.db ".backup 'aikabot-backup.db'"
```

Run only one AikaBot process against a SQLite file. Do not place the database on NFS and do not horizontally scale this deployment without first moving to a client/server database.

## Environment variables

| Variable | Required | Description |
|---|---:|---|
| `BOT_TOKEN` | yes | Telegram Bot API token; server-side only |
| `BOT_USERNAME` | yes | Bot username without `@` |
| `MINI_APP_URL` | yes | Public Mini App URL; HTTPS is enforced in production |
| `MINI_APP_ORIGIN` | no | Exact allowed CORS origin; derived from `MINI_APP_URL` by default |
| `DATABASE_PATH` | no | SQLite path, default `./data/aikabot.db` |
| `PROFILE_PHOTO_DIR` | no | Profile image directory, default `/profile_photo`; created automatically with mode `0755` |
| `ADMIN_TELEGRAM_IDS` | no | Comma-separated verified Telegram user IDs |
| `AUTH_MAX_AGE` | no | Maximum `initData` age, default `24h` |
| `LIKE_RATE_PER_MINUTE` | no | Per-sender burst rate, default `5` |
| `ACTION_COOLDOWN` | no | Per-target window for a like and, separately, for a message; default `30m` |
| `MAX_PROFILE_PHOTOS` | no | Photos per user, default `3` |
| `WEB_DIR` | no | Built frontend directory, default `./web/dist` |
| `APP_ENV` | no | Use `production` to require HTTPS |
| `LOCAL_DEV` and `DEV_TELEGRAM_*` | local only | Isolated browser-development identity |

Never put the bot token, admin IDs, or database secrets in Vite variables or frontend code.

## Profile photos and selfies

The onboarding form offers two native mobile actions: **choose from gallery** and **take a selfie**. The selfie input uses `capture="user"`, which opens the front-facing camera directly on supported Telegram/iOS/Android WebViews.

Before upload, the Mini App scales the image to at most 1600 pixels on its longest side and converts it to JPEG. The authenticated server endpoint accepts one multipart `photo`, limits the request to 8 MB, allows only decodable JPEG/PNG images, and rejects excessive dimensions. It then decodes and re-encodes the image as JPEG, removing EXIF metadata such as embedded GPS coordinates.

Each user may keep up to `MAX_PROFILE_PHOTOS` photos (default 3). The first photo in the gallery is the primary one: promoting or reordering photos rewrites `users.custom_photo_url` in the same transaction, so the avatar shown in lists, public profiles and Telegram notifications always matches `photos[0]`.

Photos are atomically stored under the authenticated owner's directory, with a generated name and a small variant for list cards:

```text
/profile_photo/users/{internal-user-uuid}/photos/{generated-uuid}.jpg
/profile_photo/users/{internal-user-uuid}/photos/{generated-uuid}_t.jpg
```

The user ID always comes from the authenticated request context; no part of a stored path is ever taken from the request body. Reads accept only the two layouts above, verified by pattern and then re-checked against the photo root, so a traversal attempt is refused before any filesystem call. The historical flat layout `/profile_photo/{internal-user-uuid}.jpg` is still served for photos uploaded before the gallery existed.

The directory is created at application startup with `os.MkdirAll(..., 0755)`. Local development uses `PROFILE_PHOTO_DIR=./profile_photo` from `.env.example`; Docker uses a persistent volume mounted at `/profile_photo`. Public photo URLs contain only an opaque UUID filename and expose no filesystem traversal or Telegram ID.

## BotFather setup

Use [@BotFather](https://t.me/BotFather) and the same HTTPS URL configured as `MINI_APP_URL`:

1. Create or select the bot and save the token only on the server.
2. Open **Bot Settings → Configure Mini App / Main Mini App**, create the Main Mini App, and set its title, description, media, and URL.
3. Set the Mini App's allowed production domain in the Web App/domain settings. The origin must match `MINI_APP_ORIGIN` exactly.
4. Use `/setmenubutton` (or **Bot Settings → Menu Button**) and set the text and Mini App URL. AikaBot also calls `setChatMenuButton` at startup and localizes it after `/start` where practical.
5. Use `/setcommands` with at least `start - Open AikaBot`. The process also registers localized `ru`, `kk`, and `en` command descriptions.
6. Enable the bot profile's Main Mini App launch button.

Supported launch forms include the profile Main Mini App button, menu button, `/start` Web App keyboard button, inline/keyboard Web App buttons where Telegram supports them, and links such as:

```text
https://t.me/BOT_USERNAME?startapp
https://t.me/BOT_USERNAME?startapp=profile_UUID
```

Do not append `mode=compact`. Telegram opens Main Mini Apps at full height by default; the client additionally calls `ready()`, `expand()`, and `requestFullscreen()` where supported.

The implementation follows Telegram's official [Mini Apps documentation](https://core.telegram.org/bots/webapps) and [Bot API](https://core.telegram.org/bots/api).

## Authentication flow

1. The frontend reads the complete raw `Telegram.WebApp.initData` string.
2. It sends that value as `Authorization: tma <raw-init-data>`; it never sends a standalone Telegram user ID.
3. The backend parses the query string, rejects duplicate fields, creates Telegram's alphabetically sorted data-check string, and validates the HMAC-SHA-256 hash using the bot token-derived `WebAppData` secret.
4. The backend validates `auth_date`, rejects future or expired data, and parses the Telegram user only after successful verification.
5. `initDataUnsafe` is used only for non-authoritative client rendering and start-parameter discovery.

Raw `initData` and exact user coordinates are not logged.

## User upsert behavior

`POST /api/auth/telegram` performs `INSERT ... ON CONFLICT (telegram_user_id) DO UPDATE`. A first open creates the internal UUID and Telegram identity. Later opens keep the same UUID and application-edited fields while refreshing mutable Telegram fields and `last_seen_at`.

The bot's private `/start` handler performs the same upsert and stores the explicit private `telegram_chat_id`. Keeping `telegram_user_id` and `telegram_chat_id` separate allows a future, separately authorized broadcast module to select eligible chat IDs. No broadcast UI or delivery loop exists in this MVP.

## Location privacy

Location is requested only after the user presses the explanation card's action. The frontend initializes Telegram `LocationManager` first and falls back to browser geolocation. Denial, timeout, unavailable services, and unsupported clients have distinct UI states.

Latitude and longitude remain server-side. Nearby results contain only a distance rounded to 0.1 km. The Haversine calculation excludes the current user, blocked/inactive/incomplete users, and users without a location before sorting and pagination.

## API

All `/api` routes except `POST /api/auth/telegram` require a valid authorization header and an existing user row. JSON bodies reject unknown fields and are size-limited.

| Method | Route | Purpose |
|---|---|---|
| `POST` | `/api/auth/telegram` | Validate Telegram and upsert the user |
| `GET` | `/api/me` | Current user's safe profile and server-derived `is_admin` |
| `PATCH` | `/api/me` | Update onboarding/profile fields and active state |
| `POST` | `/api/me/location` | Store the current user's latitude/longitude |
| `POST` | `/api/me/photo` | Legacy single-avatar upload; now replaces the primary gallery photo |
| `GET` | `/api/me/photos` | Current user's gallery and the configured maximum |
| `POST` | `/api/me/photos` | Add one JPEG/PNG multipart photo; 409 `photo_limit_reached` at the maximum |
| `PATCH` | `/api/me/photos/order` | Reorder; the body must list exactly the caller's photo IDs |
| `PATCH` | `/api/me/photos/{uuid}/primary` | Promote one photo to the front of the gallery |
| `DELETE` | `/api/me/photos/{uuid}` | Delete one photo; refuses the last one when no Telegram avatar exists |
| `GET` | `/api/users/nearby?radius_km=20&page=1&limit=20&gender=female` | Paginated nearby profiles; radius must be 5, 10, 20, or 500 |
| `GET` | `/api/users/{uuid}` | Safe public profile with its gallery, used by Mini App deep links |
| `GET` | `/api/users/{uuid}/photos` | Another user's gallery, subject to the same visibility rules |
| `GET` | `/api/users/{uuid}/cooldowns` | The caller's own like/message deadlines towards that user, plus server time |
| `POST` | `/api/users/{uuid}/like` | Send a like; `{}` is the like action, `{"message":"..."}` performs the message action instead |
| `POST` | `/api/users/{uuid}/message` | Send a personal message of up to 300 characters |
| `GET` | `/api/admin/stats` | Admin-only aggregate statistics |
| `GET` | `/api/admin/users?search=...` | Admin-only searchable user list without exact coordinates |
| `GET` | `/health` | SQLite connectivity health check |

Errors have a stable shape:

```json
{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "Too many likes. Try again in a minute."
  }
}
```

A refused action additionally reports its deadline, both inside the envelope and at the top level, so a client can start a countdown from one response:

```json
{
  "error": { "code": "like_cooldown_active", "message": "...", "next_allowed_at": "2026-08-05T16:30:00Z", "retry_after_seconds": 1437 },
  "success": false,
  "code": "like_cooldown_active",
  "action": "like",
  "next_allowed_at": "2026-08-05T16:30:00Z",
  "retry_after_seconds": 1437,
  "server_time": "2026-08-05T16:06:03Z"
}
```

`GET /api/users/nearby` sends an `ETag` and honours `If-None-Match`, answering an unchanged neighbourhood with `304 Not Modified` and no body. The tag covers the profiles, their photos and the caller's cooldown deadlines — everything except the clock — so the two-second refresh does not re-download unchanged data or re-request images.

Admin authorization is determined only from the verified Telegram ID and `ADMIN_TELEGRAM_IDS`. Ordinary users receive HTTP 403 and never receive admin data or an admin navigation entry.

## Action cooldowns

A like and a message each have their own timer per (sender, recipient) pair, so one never blocks the other. Enforcement is entirely server-side.

Each attempt claims its timer with a single `INSERT ... ON CONFLICT DO UPDATE ... WHERE next_allowed_at <= now RETURNING ...` inside a transaction. Two concurrent requests therefore cannot both observe an expired row and both write a fresh one: the second one's `WHERE` is evaluated against the first one's committed value, and it is refused with the active deadline. Double taps, retries, a second device and direct API calls all funnel through the same statement.

The claim is taken *before* the Telegram notification is sent, so a duplicate that arrives while the first is still in flight is refused rather than delivered twice. If delivery then fails, the claim is released — matching the deadline it wrote, so a newer claim is never discarded — because an action that was never delivered should not cost the sender a full window.

Deadlines are stored as Unix milliseconds and reported as RFC3339 server time. The Mini App measures its offset from the server clock and renders every countdown against that, so a wrong device clock cannot show a timer that disagrees with what the server will allow.

## Production deployment

1. Point a dedicated HTTPS domain at a reverse proxy such as Caddy or nginx.
2. Copy `.env.example` to `.env`, set `APP_ENV=production`, real Telegram settings, an exact HTTPS Mini App origin, and admin IDs.
3. Start the single container:

   ```bash
   docker compose up -d --build
   ```

4. Proxy HTTPS traffic to `127.0.0.1:8080` and set `X-Forwarded-Proto: https`. The production middleware rejects non-HTTPS application traffic.
5. Keep the named `aika_data` volume persistent and schedule tested SQLite backups.
6. Monitor `/health`, container restarts, disk usage, and Bot API errors. Restore backups in a staging copy before relying on them.

The container runs as an unprivileged user, serves the prebuilt Mini App, stores SQLite at `/app/data/aikabot.db`, persists normalized images in the `aika_profile_photos` volume mounted at `/profile_photo`, and has a health check. Only the reverse proxy should be publicly exposed.

### systemd service on `/home/aikaDating`

The included `aikas.service` runs `make run` with Node.js 22 from root's nvm installation, keeps the process in the background, restarts it after failures, and sends output to the system journal. Stop any foreground `make run` first so port `8089` is free, then install the unit as root:

```bash
cd /home/aikaDating
cp aikas.service /etc/systemd/system/aikas.service
systemctl daemon-reload
systemctl enable --now aikas.service
systemctl status aikas.service --no-pager
```

Follow logs and restart after configuration changes with:

```bash
journalctl -u aikas.service -f
systemctl restart aikas.service
```

The unit assumes nvm is installed at `/root/.nvm` and the repository is located at `/home/aikaDating`.

## Tests and builds

```bash
make test          # go test ./... , tsc --noEmit , node --test
go vet ./...
cd web && npm ci && npm run build
docker compose config
```

Frontend tests run on Node's own test runner and type stripping (`node --test 'src/**/*.test.ts'`), so they add no dependency to the project.

Go tests cover Telegram HMAC validation and expiration, SQLite upsert uniqueness, migration idempotency and rollback, admin allowlisting, Haversine distance, radius/current-user/blocked-user filtering, self-like rejection, short-message validation, profile photo storage/upload/delivery, gallery ordering, primary-photo promotion, cross-account photo rejection, path-traversal rejection, cooldown windows and their independence, concurrent claims collapsing to one action, entity-tag revalidation, and escaped localized Telegram notification formatting.

TypeScript tests cover calendar-date parsing and formatting across timezones, leap years, age boundaries, countdown maths and formatting, cooldown merging, the polling loop's non-overlap, abort-on-stop, backoff and recovery, and list merging that neither duplicates nor reorders cards.
# aikaDating
