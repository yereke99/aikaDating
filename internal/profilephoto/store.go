package profilephoto

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	MaxUploadBytes = 8 << 20
	maxDimension   = 4096
	maxPixels      = 10_000_000
	// Stored photos are capped well below the upload limit: the carousel never needs more, and a
	// smaller file keeps the 2-second nearby refresh cheap on a mobile connection.
	maxStoredSide = 1440
	thumbnailSide = 320
	// URLPrefix is the single public mount point for every stored photo. The HTTP route and the
	// URL written into the database are both derived from it, so they cannot drift apart.
	URLPrefix = "/profile_photo/"
	MIMEType  = "image/jpeg"
)

var (
	ErrInvalidImage = errors.New("invalid profile image")
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	// The two shapes a stored photo may have: the historical flat avatar, and a photo owned by one
	// user's directory. Anything else — traversal, absolute paths, a foreign extension — is refused
	// before a filesystem call is made.
	legacyPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.jpg$`)
	ownedPattern  = regexp.MustCompile(`^users/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/photos/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}(_t)?\.jpg$`)
)

// Stored describes a photo that has been normalised and written to disk.
type Stored struct {
	RelativePath string
	PublicURL    string
	ThumbnailURL string
	Width        int
	Height       int
	MIMEType     string
}

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
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve profile photo directory: %w", err)
	}
	return &Store{directory: absolute}, nil
}

// Save keeps the original single-avatar layout: one file per user, replaced in place. It remains
// the storage used by the legacy POST /api/me/photo endpoint.
func (s *Store) Save(userID string, uploaded []byte) (string, error) {
	if !uuidPattern.MatchString(userID) {
		return "", ErrInvalidImage
	}
	decoded, err := decode(uploaded)
	if err != nil {
		return "", err
	}
	if err := s.write(userID+".jpg", decoded, maxStoredSide); err != nil {
		return "", err
	}
	return URLPrefix + userID + ".jpg", nil
}

// SaveUserPhoto writes one photo into the authenticated user's own directory under a generated
// name. The caller supplies the user ID from server-side context only — no part of the path is
// ever taken from the request.
func (s *Store) SaveUserPhoto(userID string, uploaded []byte) (Stored, error) {
	if !uuidPattern.MatchString(userID) {
		return Stored{}, ErrInvalidImage
	}
	decoded, err := decode(uploaded)
	if err != nil {
		return Stored{}, err
	}
	name, err := newUUID()
	if err != nil {
		return Stored{}, err
	}
	directory := path.Join("users", userID, "photos")
	if err := os.MkdirAll(filepath.Join(s.directory, filepath.FromSlash(directory)), 0o755); err != nil {
		return Stored{}, fmt.Errorf("create user photo directory: %w", err)
	}
	relative := path.Join(directory, name+".jpg")
	if err := s.write(relative, decoded, maxStoredSide); err != nil {
		return Stored{}, err
	}
	thumbnail := path.Join(directory, name+"_t.jpg")
	if err := s.write(thumbnail, decoded, thumbnailSide); err != nil {
		// The full-size photo is already usable; a missing thumbnail must not fail the upload.
		thumbnail = relative
	}
	bounds := decoded.Bounds()
	return Stored{
		RelativePath: relative,
		PublicURL:    URLPrefix + relative,
		ThumbnailURL: URLPrefix + thumbnail,
		Width:        bounds.Dx(),
		Height:       bounds.Dy(),
		MIMEType:     MIMEType,
	}, nil
}

// Remove deletes a stored photo and its thumbnail. The path must be one this store could have
// produced; anything else is ignored rather than followed.
func (s *Store) Remove(relativePath string) error {
	target, ok := s.FilePath(relativePath)
	if !ok {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if thumbnail, ok := s.FilePath(ThumbnailPath(relativePath)); ok && thumbnail != target {
		if err := os.Remove(thumbnail); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ThumbnailPath maps a stored photo path to its thumbnail sibling. Legacy flat avatars have none,
// so their own path is returned.
func ThumbnailPath(relativePath string) string {
	if !ownedPattern.MatchString(relativePath) || strings.HasSuffix(relativePath, "_t.jpg") {
		return relativePath
	}
	return strings.TrimSuffix(relativePath, ".jpg") + "_t.jpg"
}

// FilePath resolves a public path to a file on disk. It returns false for anything that does not
// match an expected layout, and verifies the resolved path really is inside the photo root so a
// future layout change cannot reintroduce a traversal.
func (s *Store) FilePath(relativePath string) (string, bool) {
	relativePath = strings.TrimPrefix(relativePath, "/")
	if !legacyPattern.MatchString(relativePath) && !ownedPattern.MatchString(relativePath) {
		return "", false
	}
	resolved := filepath.Join(s.directory, filepath.FromSlash(relativePath))
	inside, err := filepath.Rel(s.directory, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolved, true
}

// OwnedBy reports whether a stored path belongs to the given user, so a delete or reorder can be
// refused even if a row were somehow addressed across accounts.
func OwnedBy(relativePath, userID string) bool {
	if relativePath == "" {
		return true
	}
	if legacyPattern.MatchString(relativePath) {
		return strings.TrimSuffix(relativePath, ".jpg") == userID
	}
	return strings.HasPrefix(relativePath, "users/"+userID+"/photos/") && ownedPattern.MatchString(relativePath)
}

func decode(uploaded []byte) (image.Image, error) {
	configuration, format, err := image.DecodeConfig(bytes.NewReader(uploaded))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, ErrInvalidImage
	}
	if configuration.Width < 64 || configuration.Height < 64 ||
		configuration.Width > maxDimension || configuration.Height > maxDimension ||
		configuration.Width*configuration.Height > maxPixels {
		return nil, ErrInvalidImage
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(uploaded))
	if err != nil || decodedFormat != format {
		return nil, ErrInvalidImage
	}
	return decoded, nil
}

// write encodes to a temporary file in the destination directory and renames it into place, so a
// reader never observes a half-written photo and a failed encode leaves the previous file intact.
func (s *Store) write(relativePath string, decoded image.Image, maxSide int) error {
	destination, ok := s.FilePath(relativePath)
	if !ok {
		return ErrInvalidImage
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".profile-photo-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary profile photo: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := jpeg.Encode(temporary, downscale(decoded, maxSide), &jpeg.Options{Quality: 86}); err != nil {
		temporary.Close()
		return fmt.Errorf("encode profile photo: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync profile photo: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set profile photo permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close profile photo: %w", err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("publish profile photo: %w", err)
	}
	return nil
}

// downscale box-filters the source down to maxSide. Averaging every source pixel that lands in a
// destination pixel avoids the aliasing a nearest-neighbour shrink would produce on a portrait.
func downscale(source image.Image, maxSide int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longest := max(width, height)
	if longest <= maxSide || longest == 0 {
		return source
	}
	targetWidth := max(1, width*maxSide/longest)
	targetHeight := max(1, height*maxSide/longest)
	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := range targetHeight {
		startY := bounds.Min.Y + y*height/targetHeight
		endY := max(startY+1, bounds.Min.Y+(y+1)*height/targetHeight)
		for x := range targetWidth {
			startX := bounds.Min.X + x*width/targetWidth
			endX := max(startX+1, bounds.Min.X+(x+1)*width/targetWidth)
			var sumR, sumG, sumB, sumA, count uint64
			for sampleY := startY; sampleY < endY; sampleY++ {
				for sampleX := startX; sampleX < endX; sampleX++ {
					r, g, b, a := source.At(sampleX, sampleY).RGBA()
					sumR += uint64(r)
					sumG += uint64(g)
					sumB += uint64(b)
					sumA += uint64(a)
					count++
				}
			}
			if count == 0 {
				continue
			}
			target.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / count >> 8),
				G: uint8(sumG / count >> 8),
				B: uint8(sumB / count >> 8),
				A: uint8(sumA / count >> 8),
			})
		}
	}
	return target
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
