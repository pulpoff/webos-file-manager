package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tree makes empty files (and their folders) under root.
func tree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func pickNames(ps []pick) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.rel)
	}
	return strings.Join(out, " ")
}

func TestFolderPicks(t *testing.T) {
	root := t.TempDir()
	tree(t, root,
		"Movies/b.mkv", "Movies/a.mp4", "Movies/poster.jpg",
		"Camera/1.jpg", "Camera/2.jpg", "Camera/3.jpg", "Camera/4.jpg", "Camera/5.jpg",
		"Series/Season 2/e1.mkv", "Series/Season 1/e1.mkv", "Series/Season 1/e2.mkv",
		"Photos/2023/Beach/x.jpg", "Photos/2024/Trip/c.jpg", "Photos/2024/Trip/a.jpg",
		"Albums/One/a.jpg", "Albums/Two/b.png", "Albums/Two/c.png", "Albums/Two/d.png", "Albums/Two/e.png",
		"Deep/1/2/3/a.jpg",
		"Hidden/.thumbs/a.jpg", "Hidden/$RECYCLE.BIN/b.mkv",
		"Empty/notes.txt",
	)
	fs := &localFS{root}
	ctx := context.Background()
	cases := []struct {
		dir    string
		ffmpeg string
		want   string
	}{
		{"Movies", "ffmpeg", "Movies/a.mp4 Movies/b.mkv Movies/poster.jpg"}, // videos first, then photos
		{"Movies", "", "Movies/a.mp4 Movies/b.mkv"},                         // no ffmpeg, no photo thumbnails
		{"Camera", "ffmpeg", "Camera/1.jpg Camera/2.jpg Camera/3.jpg Camera/4.jpg"},
		{"Series", "ffmpeg", "Series/Season 1/e1.mkv Series/Season 1/e2.mkv Series/Season 2/e1.mkv"},
		{"Albums", "ffmpeg", "Albums/One/a.jpg Albums/Two/b.png Albums/Two/c.png Albums/Two/d.png"}, // photos one level down
		{"Photos", "ffmpeg", "Photos/2023/Beach/x.jpg Photos/2024/Trip/a.jpg Photos/2024/Trip/c.jpg"}, // two levels down
		{"Photos", "", ""},
		{"Deep", "ffmpeg", ""},   // three levels down is too deep
		{"Hidden", "ffmpeg", ""}, // dot and system folders are skipped
		{"Empty", "ffmpeg", ""},
	}
	for _, c := range cases {
		m := &media{ffmpeg: c.ffmpeg}
		ps, err := m.folderPicks(ctx, fs, c.dir)
		if err != nil {
			t.Fatalf("%s: %v", c.dir, err)
		}
		if got := pickNames(ps); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s (ffmpeg %q):\n got  %q\n want %q", c.dir, c.ffmpeg, got, c.want)
		}
	}
}

// A wide library stops after previewLists listings.
func TestFolderPicksListingBudget(t *testing.T) {
	root := t.TempDir()
	var files []string
	for _, a := range []string{"a", "b", "c", "d"} {
		for _, b := range []string{"1", "2", "3", "4"} {
			files = append(files, "Lib/"+a+"/"+b+"/readme.txt")
		}
	}
	tree(t, root, files...)
	n := 0
	fs := &countingFS{FS: &localFS{root}, n: &n}
	if _, err := (&media{ffmpeg: "ffmpeg"}).folderPicks(context.Background(), fs, "Lib"); err != nil {
		t.Fatal(err)
	}
	if n != 1+previewLists {
		t.Errorf("listed %d folders, want %d", n, 1+previewLists)
	}
}

type countingFS struct {
	FS
	n *int
}

func (c *countingFS) List(ctx context.Context, rel string) ([]Entry, error) {
	*c.n++
	return c.FS.List(ctx, rel)
}
