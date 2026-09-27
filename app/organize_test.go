package app

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sollniss/camsync"
	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
)

// libraryDir builds a small library: a trip folder for the Canon, the
// phone's photos of the same trip still in the inbox.
func libraryDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	photo := func(rel, mk, model, dto string, pos *[2]float64) {
		ifd0 := []testimg.Entry{testimg.ASCII(exif.TagMake, mk), testimg.ASCII(exif.TagModel, model)}
		if pos != nil {
			ifd0 = append(ifd0, testimg.GPSAt(binary.LittleEndian, "2024:05:02", 10, 0, 0, pos[0], pos[1]))
		}
		tiff := testimg.BuildTIFF(binary.LittleEndian, ifd0, []testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, dto)})
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, path, testimg.JPEG(tiff))
	}
	away := &[2]float64{54.42, 13.43}
	for d := 1; d <= 3; d++ {
		for h := 9; h <= 12; h++ {
			photo("Trips/Rügen/c"+string(rune('0'+d))+string(rune('a'+h))+".jpg", "Canon", "EOS R5", "2024:05:0"+string(rune('0'+d))+" "+string(rune('0'+h/10))+string(rune('0'+h%10))+":00:00", nil)
		}
	}
	photo("Trips/Rügen/p1.jpg", "Apple", "iPhone 15", "2024:05:02 10:00:00", away)
	photo("Inbox/p2.jpg", "Apple", "iPhone 15", "2024:05:01 11:30:00", away)
	photo("Inbox/p3.jpg", "Apple", "iPhone 15", "2024:05:03 11:30:00", away)
	mustWrite(t, filepath.Join(root, "Inbox", "p3.jpg.xmp"), []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about=""/></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`))
	mustWrite(t, filepath.Join(root, "Inbox", "notes.txt"), []byte("x"))
	return root
}

func TestOrganizeAndApply(t *testing.T) {
	root := libraryDir(t)
	ctx := context.Background()
	res, err := (OrganizeHandler{}).Handle(ctx, OrganizeQuery{Scope: Scope{Dir: root}, Options: camsync.LibraryOptions{Inboxes: []string{"Inbox"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Photos) != 15 || res.Ignored != 1 || len(res.Events) != 1 || len(res.Folders) != 2 {
		t.Fatalf("result = %d photos, %d ignored, %d events, %d folders", len(res.Photos), res.Ignored, len(res.Events), len(res.Folders))
	}
	if res.Options.Inboxes[0] != filepath.Join(root, "Inbox") {
		t.Errorf("inboxes = %v", res.Options.Inboxes)
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0].Kind != camsync.SuggestMove || len(res.Suggestions[0].Photos) != 2 {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	s := res.Suggestions[0]
	if s.To != filepath.Join(root, "Trips", "Rügen") || s.Photos[1].Sidecar == "" {
		t.Errorf("suggestion = %+v", s)
	}

	// Replanning with a tiny gap splits the days and changes the picture
	// without scanning again.
	again := res.With(camsync.LibraryOptions{EventOptions: camsync.EventOptions{MaxGap: 1}})
	if len(again.Events) < 3 || len(again.Photos) != 15 {
		t.Errorf("replanned = %d events", len(again.Events))
	}

	plan := camsync.Plan{Root: root, Moves: s.Moves()}
	apply, err := (ApplyHandler{}).Handle(ctx, ApplyCommand{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if apply.Moved != 2 || apply.Failed != 0 {
		t.Errorf("apply = %+v", apply)
	}
	for _, name := range []string{"p2.jpg", "p3.jpg", "p3.jpg.xmp"} {
		if _, err := os.Stat(filepath.Join(root, "Trips", "Rügen", name)); err != nil {
			t.Error(err)
		}
	}
	res, err = (OrganizeHandler{}).Handle(ctx, OrganizeQuery{Scope: Scope{Dir: root}})
	if err != nil || len(res.Suggestions) != 0 {
		t.Errorf("after apply: %v, %d suggestions", err, len(res.Suggestions))
	}

	if _, err := (ApplyHandler{}).Handle(ctx, ApplyCommand{Plan: camsync.Plan{Root: root, Moves: []camsync.Move{{Path: "/etc/passwd", To: root}}}}); err == nil {
		t.Error("a move from outside the root was accepted")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Errorf("error is %T", err)
	}
	if _, err := (OrganizeHandler{}).Handle(ctx, OrganizeQuery{Scope: Scope{Dir: root}, Options: camsync.LibraryOptions{Layout: "weekly"}}); err == nil {
		t.Error("bad layout accepted")
	}
}
