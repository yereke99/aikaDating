package migrations

import _ "embed"

// UsersUp contains the sole persistent schema used by AikaBot.
//
//go:embed 000001_create_users.up.sql
var UsersUp string
