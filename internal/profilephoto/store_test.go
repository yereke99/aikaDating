package profilephoto

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

const testUserID = "8ce47d50-8f70-4e32-a575-4de37c6dd3be"

func TestStoreCreatesDirectoryAndNormalizesPhoto(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "profile_photo")
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o755 {
		t.Fatalf("directory permissions = %o, want 755", permissions)
	}

	source := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			source.Set(x, y, color.RGBA{R: 190, G: 148, B: 64, A: 255})
		}
	}
	var uploaded bytes.Buffer
	if err := png.Encode(&uploaded, source); err != nil {
		t.Fatal(err)
	}
	publicPath, err := store.Save(testUserID, uploaded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if publicPath != "/profile_photo/"+testUserID+".jpg" {
		t.Fatalf("public path = %q", publicPath)
	}
	stored, err := os.ReadFile(filepath.Join(directory, testUserID+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.Decode(bytes.NewReader(stored)); err != nil || format != "jpeg" {
		t.Fatalf("stored image format = %q, error = %v", format, err)
	}
}

func TestStoreRejectsInvalidImage(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(testUserID, []byte("not an image")); err != ErrInvalidImage {
		t.Fatalf("Save() error = %v, want %v", err, ErrInvalidImage)
	}
}
