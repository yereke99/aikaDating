package telegram

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"aika/internal/domain"
)

func TestFormatLikeNotificationEscapesUserText(t *testing.T) {
	sender := domain.User{
		DisplayName: sql.NullString{String: `<b>Aika & Co</b>`, Valid: true},
		Username:    sql.NullString{String: "aika_user", Valid: true},
		BirthDate:   sql.NullTime{Time: time.Date(2000, 8, 3, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	text := FormatLikeNotification("en", sender, `<script>alert("x")</script>`, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))
	if strings.Contains(text, "<script>") || strings.Contains(text, "<b>Aika") {
		t.Fatalf("unescaped user content: %s", text)
	}
	for _, expected := range []string{"A user liked your profile", "Age:</b> 25", "@aika_user", "&lt;script&gt;"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("notification missing %q: %s", expected, text)
		}
	}
}

func TestFormatLikeNotificationOmitsEmptyMessage(t *testing.T) {
	sender := domain.User{DisplayName: sql.NullString{String: "Aika", Valid: true}}
	text := FormatLikeNotification("ru", sender, "", time.Now())
	if strings.Contains(text, "Сообщение") {
		t.Fatalf("empty message section was not omitted: %s", text)
	}
}
