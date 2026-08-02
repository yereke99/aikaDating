package profilephoto

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
)

const (
	MaxUploadBytes = 8 << 20
	maxDimension   = 4096
	maxPixels      = 10_000_000
)

var (
	ErrInvalidImage = errors.New("invalid profile image")
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	filePattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.jpg$`)
)

type Store struct {
	directory string
}

func New(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create profile photo directory: %w", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		return nil, fmt.Errorf("set profile photo directory permissions: %w", err)
	}
	return &Store{directory: directory}, nil
}

func (s *Store) Save(userID string, uploaded []byte) (string, error) {
	if !uuidPattern.MatchString(userID) {
		return "", ErrInvalidImage
	}
	configuration, format, err := image.DecodeConfig(bytes.NewReader(uploaded))
	if err != nil || (format != "jpeg" && format != "png") {
		return "", ErrInvalidImage
	}
	if configuration.Width < 64 || configuration.Height < 64 ||
		configuration.Width > maxDimension || configuration.Height > maxDimension ||
		configuration.Width*configuration.Height > maxPixels {
		return "", ErrInvalidImage
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(uploaded))
	if err != nil || decodedFormat != format {
		return "", ErrInvalidImage
	}

	temporary, err := os.CreateTemp(s.directory, ".profile-photo-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary profile photo: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := jpeg.Encode(temporary, decoded, &jpeg.Options{Quality: 86}); err != nil {
		temporary.Close()
		return "", fmt.Errorf("encode profile photo: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync profile photo: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return "", fmt.Errorf("set profile photo permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close profile photo: %w", err)
	}
	filename := userID + ".jpg"
	if err := os.Rename(temporaryName, filepath.Join(s.directory, filename)); err != nil {
		return "", fmt.Errorf("publish profile photo: %w", err)
	}
	return "/profile_photo/" + filename, nil
}

func (s *Store) FilePath(filename string) (string, bool) {
	if !filePattern.MatchString(filename) {
		return "", false
	}
	return filepath.Join(s.directory, filename), true
}
