package users

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"aika/internal/domain"
)

func nearbyUser(id string, lat, lon float64) domain.User {
	return domain.User{
		ID: id, DisplayName: sql.NullString{String: id, Valid: true},
		Latitude: sql.NullFloat64{Float64: lat, Valid: true}, Longitude: sql.NullFloat64{Float64: lon, Valid: true},
		IsActive: true, IsProfileCompleted: true,
	}
}

func TestHaversineRadius(t *testing.T) {
	distance := HaversineKM(43.238949, 76.889709, 43.2220, 76.8512)
	if distance < 3 || distance > 4.5 {
		t.Fatalf("HaversineKM() = %.2f, expected an Almaty distance around 3.6 km", distance)
	}
}

func TestFilterNearbyExcludesCurrentAndBlockedUsers(t *testing.T) {
	current := nearbyUser("current", 43.238949, 76.889709)
	blocked := nearbyUser("blocked", 43.24, 76.89)
	blocked.IsBlocked = true
	far := nearbyUser("far", 51.1694, 71.4491)
	close := nearbyUser("close", 43.24, 76.89)

	page := FilterNearby(current, []domain.User{current, blocked, far, close}, 20, "", 1, 20, time.Now())
	if len(page.Users) != 1 || page.Users[0].ID != "close" {
		t.Fatalf("unexpected nearby users: %+v", page.Users)
	}
}

func TestValidateLike(t *testing.T) {
	if _, err := ValidateLike("same", "same", nil); !errors.Is(err, ErrSelfLike) {
		t.Fatalf("self-like error = %v", err)
	}
	empty := "   "
	if _, err := ValidateLike("sender", "recipient", &empty); !errors.Is(err, ErrMessageRequired) {
		t.Fatalf("empty message error = %v", err)
	}
	longRunes := make([]rune, 301)
	for i := range longRunes {
		longRunes[i] = 'я'
	}
	long := string(longRunes)
	if _, err := ValidateLike("sender", "recipient", &long); !errors.Is(err, ErrMessageTooLong) {
		t.Fatalf("long message error = %v", err)
	}
}
