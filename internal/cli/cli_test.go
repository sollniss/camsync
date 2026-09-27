package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sollniss/camsync/exif"
	"github.com/sollniss/camsync/internal/testimg"
	"github.com/sollniss/camsync/xmp"
)

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func setup(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "IMG_0001.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:00:00")))
	mustWrite(t, filepath.Join(dir, "IMG_0002.JPG"), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", "2024:05:01 12:10:00")))
	mustWrite(t, filepath.Join(dir, "DSC_0001.JPG"), testimg.JPEG(testimg.Simple("SONY", "ILCE-7M3", "2024:05:01 12:00:30")))
	mustWrite(t, filepath.Join(dir, "notes.txt"), []byte("hello"))
	return dir
}

func TestListTableAndJSON(t *testing.T) {
	dir := setup(t)
	code, out, errOut := run(t, "list", "-v", dir)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"Canon EOS R5", "SONY ILCE-7M3", "IMG_0002.JPG", "3 photos, 2 cameras, 1 other files ignored"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}

	code, out, _ = run(t, "ls", "--output", "json", dir)
	if code != 0 {
		t.Fatal(out)
	}
	var v struct {
		Cameras []struct {
			Index  int                               `json:"index"`
			Camera string                            `json:"camera"`
			Files  int                               `json:"files"`
			First  string                            `json:"first"`
			Offset string                            `json:"offset"`
			Photos []struct{ Path, Original string } `json:"photos"`
		} `json:"cameras"`
		Photos  int `json:"photos"`
		Ignored int `json:"ignored"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Photos != 3 || v.Ignored != 1 || len(v.Cameras) != 2 || v.Cameras[0].Camera != "Canon EOS R5" || v.Cameras[0].First != "2024-05-01T12:00:00" || v.Cameras[0].Offset != "none" || len(v.Cameras[0].Photos) != 2 {
		t.Errorf("json = %+v", v)
	}
}

func TestShiftFlow(t *testing.T) {
	dir := setup(t)

	code, out, _ := run(t, "shift", "--camera", "r5", "--by", "2m30s", "--dry-run", dir)
	if code != 0 || !strings.Contains(out, "dry run:") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "IMG_0001.JPG.xmp")); err == nil {
		t.Fatal("dry run wrote a sidecar")
	}

	code, out, _ = run(t, "shift", "-c", "1", "--by", "2m30s", dir)
	if code != 0 {
		t.Fatal(out)
	}
	if !strings.Contains(out, "create  "+filepath.Join(dir, "IMG_0001.JPG")+": 2024-05-01 12:00:00 -> 2024-05-01 12:02:30  (IMG_0001.JPG.xmp)") ||
		!strings.Contains(out, `"Canon EOS R5" shifted by +2m30s: 2 sidecars written (2 new), 0 skipped, 0 failed`) {
		t.Errorf("shift output:\n%s", out)
	}

	code, out, _ = run(t, "shift", "-c", "sony", "--tz", "+0200", "-o", "json", dir)
	if code != 0 {
		t.Fatal(out)
	}
	var v struct {
		Camera  string `json:"camera"`
		By      string `json:"by"`
		Zone    string `json:"zone"`
		Written int    `json:"written"`
		Entries []struct {
			Action string `json:"action"`
			After  string `json:"after"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v.Camera != "SONY ILCE-7M3" || v.By != "none" || v.Zone != "+02:00" || v.Written != 1 || v.Entries[0].Action != "create" || v.Entries[0].After != "2024-05-01T12:00:30+02:00" {
		t.Errorf("json = %+v", v)
	}

	code, out, _ = run(t, "list", "-o", "json", dir)
	if code != 0 || !strings.Contains(out, `"offset": "+2m30s"`) {
		t.Errorf("offset not visible after shift:\n%s", out)
	}

	code, out, _ = run(t, "reset", "--camera", "canon", dir)
	if code != 0 || !strings.Contains(out, `"Canon EOS R5" reset to the image timestamps: 2 sidecars written (0 new)`) {
		t.Errorf("reset: %d\n%s", code, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "IMG_0001.JPG.xmp"))
	if f, _ := xmp.Read(data); f[xmp.DateTimeOriginal] != "2024-05-01T12:00:00" {
		t.Errorf("after reset: %v", f)
	}
}

func TestErrorsAndHelp(t *testing.T) {
	dir := setup(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"shift", "--by", "1s", dir}, `required flag(s) "camera"`},
		{[]string{"shift", "-c", "r5", dir}, "--by or --timezone is required"},
		{[]string{"shift", "-c", "r5", "--by", "0", dir}, "shift must not be zero"},
		{[]string{"shift", "-c", "r5", "--by", "1s", "--timezone", "nope", dir}, "invalid UTC offset"},
		{[]string{"shift", "-c", "nikon", "--by", "1s", dir}, "no camera matches"},
		{[]string{"shift", "-c", "o", "--by", "1s", dir}, "ambiguous"},
		{[]string{"list", "--sidecar-style", "weird", dir}, "unknown sidecar style"},
		{[]string{"list", "-o", "xml", dir}, "unknown output format"},
		{[]string{"list", dir, "extra"}, "unexpected argument"},
		{[]string{"list", filepath.Join(dir, "missing")}, "no such file"},
		{[]string{"bogus"}, `unknown command "bogus"`},
	}
	for _, c := range cases {
		code, _, errOut := run(t, c.args...)
		if code != ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %q (want %q)", c.args, code, errOut, c.want)
		}
	}
	code, out, _ := run(t, "--help")
	if code != 0 || !strings.Contains(out, "camsync shift --camera sony") {
		t.Errorf("help: %d\n%s", code, out)
	}
	code, out, _ = run(t, "shift", "--help")
	if code != 0 || !strings.Contains(out, "--dry-run") {
		t.Errorf("shift help: %d\n%s", code, out)
	}
}

func TestConfigAndEnv(t *testing.T) {
	dir := setup(t)
	t.Setenv("CAMSYNC_SIDECAR_STYLE", "name")
	if code, out, _ := run(t, "shift", "-c", "sony", "--by", "1s", dir); code != 0 || !strings.Contains(out, "(DSC_0001.xmp)") {
		t.Errorf("env sidecar style: %d\n%s", code, out)
	}
	t.Setenv("CAMSYNC_SIDECAR_STYLE", "") // env would beat the file
	cfg := filepath.Join(t.TempDir(), "cfg.yaml")
	mustWrite(t, cfg, []byte("output: json\nsidecar-style: ext\n"))
	code, out, _ := run(t, "--config", cfg, "shift", "-c", "canon", "--by", "1s", dir)
	if code != 0 || !strings.Contains(out, `"created": 2`) {
		t.Errorf("config file: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "IMG_0001.JPG.xmp")); err != nil {
		t.Error("config file sidecar style not applied")
	}
	// Flag beats env.
	if code, out, _ := run(t, "list", "--sidecar-style", "ext", "-o", "json", dir); code != 0 || strings.Contains(out, "DSC_0001.xmp") == false {
		t.Errorf("existing name-style sidecar must still be found: %d\n%s", code, out)
	}
}

func TestCompletion(t *testing.T) {
	dir := setup(t)
	code, out, _ := run(t, "__complete", "shift", dir, "--camera", "")
	if code != 0 || !strings.Contains(out, "Canon EOS R5\t2 files") || !strings.Contains(out, "2\tSONY ILCE-7M3, 1 files") {
		t.Errorf("camera completion: %d\n%s", code, out)
	}
	code, out, _ = run(t, "__complete", "list", "--sidecar-style", "")
	if code != 0 || !strings.Contains(out, "ext\t") || !strings.Contains(out, "name\t") {
		t.Errorf("sidecar-style completion: %d\n%s", code, out)
	}
	code, out, _ = run(t, "__complete", "list", "")
	if code != 0 || !strings.Contains(out, ":16") /* ShellCompDirectiveFilterDirs */ {
		t.Errorf("dir completion: %d\n%s", code, out)
	}
	code, out, _ = run(t, "completion", "zsh")
	if code != 0 || !strings.HasPrefix(out, "#compdef camsync") {
		t.Errorf("completion script: %d\n%.80s", code, out)
	}
}

func TestSingleDashHints(t *testing.T) {
	dir := setup(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"shift", "-camera", "Canon PowerShot G9 X", "--tz", "+9:00", dir}, `-camera was read as -c with the value "amera"; flags take two dashes: --camera "Canon PowerShot G9 X"`},
		{[]string{"shift", "-c", "r5", "-by", "1s", dir}, "flags take two dashes: --by"},
		{[]string{"list", "-recursive", dir}, "flags take two dashes: --recursive"},
		{[]string{"shift", "-c", "r5", "--by", "1s", "-dry-run", dir}, "flags take two dashes: --dry-run"},
		{[]string{"list", "-output", "json", dir}, `-output was read as -o with the value "utput"; flags take two dashes: --output "json"`},
		{[]string{"list", dir, "extra", "more"}, `unexpected argument "extra", "more": only one DIR may be given`},
	}
	for _, c := range cases {
		code, _, errOut := run(t, c.args...)
		if code != ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v:\n  got  %q\n  want %q", c.args, strings.TrimSpace(errOut), c.want)
		}
	}
}

func TestErrorEndsWithNewline(t *testing.T) {
	_, _, errOut := run(t, "list", filepath.Join(t.TempDir(), "missing"))
	if !strings.HasSuffix(errOut, "\n") {
		t.Errorf("stderr must end with a newline: %q", errOut)
	}
}

func TestCompletionIgnoresBrokenConfig(t *testing.T) {
	dir := setup(t)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "camsync")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(cfgDir, "config.yaml"), []byte("output: bogus\n"))
	if code, _, errOut := run(t, "list", dir); code != ExitUsage || !strings.Contains(errOut, "unknown output format") {
		t.Fatalf("broken config must still fail normal commands: %d %q", code, errOut)
	}
	if code, out, errOut := run(t, "completion", "bash"); code != 0 || errOut != "" || !strings.Contains(out, "camsync") {
		t.Errorf("completion script with broken config: %d %q", code, errOut)
	}
	// cobra reports the directive on stderr for __complete; that is not an error.
	if code, out, errOut := run(t, "__complete", "shift", dir, "--camera", ""); code != 0 || strings.Contains(errOut, "error:") || !strings.Contains(out, "Canon EOS R5") {
		t.Errorf("camera completion with broken config: %d %q\n%s", code, errOut, out)
	}
}

func TestZeroShiftWithZone(t *testing.T) {
	dir := setup(t)
	code, out, errOut := run(t, "shift", "-c", "sony", "--by", "0", "--timezone", "+02:00", "-o", "json", dir)
	if code != 0 || !strings.Contains(out, `"zone": "+02:00"`) || !strings.Contains(out, `"by": "none"`) {
		t.Errorf("zero shift with zone: %d %q\n%s", code, errOut, out)
	}
	if code, _, errOut := run(t, "shift", "-c", "sony", "--by", "0s", dir); code != ExitUsage || !strings.Contains(errOut, "shift must not be zero") {
		t.Errorf("zero shift alone: %d %q", code, errOut)
	}
}

func TestExitCodes(t *testing.T) {
	dir := setup(t)

	// Usage errors get a pointer to --help; failures do not.
	code, _, errOut := run(t, "shift", "-c", "nikon", "--by", "1s", dir)
	if code != ExitUsage || !strings.Contains(errOut, "Run 'camsync shift --help' for usage.") {
		t.Errorf("unknown camera: %d %q", code, errOut)
	}

	// A sidecar that cannot be written is a failure, and the JSON result is
	// still printed so scripts can see which photo failed.
	if err := os.Mkdir(filepath.Join(dir, "DSC_0001.JPG.xmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "shift", "-c", "sony", "--by", "1s", "-o", "json", dir)
	if code != ExitFailure || !strings.Contains(errOut, "1 photos could not be processed") || strings.Contains(errOut, "--help") {
		t.Errorf("write failure: %d %q", code, errOut)
	}
	if !strings.Contains(out, `"action": "fail"`) {
		t.Errorf("json result missing on failure:\n%s", out)
	}

	// Cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Main(ctx, []string{"list", dir}, &stdout, &stderr); code != ExitInterrupted {
		t.Errorf("cancelled: %d %q", code, stderr.String())
	}
}

// tripDir writes photos of the same moments from a phone with GPS and a
// Canon whose clock runs 10m4s behind.
func tripDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	start := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	for i, gap := range []int{0, 1300, 700, 2900, 400, 3000, 1800, 900} {
		at := start.Add(time.Duration(i*3600+gap) * time.Second)
		utc := at.Add(-2 * time.Hour)
		phone := testimg.BuildTIFF(binary.LittleEndian,
			[]testimg.Entry{testimg.ASCII(exif.TagMake, "Apple"), testimg.ASCII(exif.TagModel, "iPhone 15"), testimg.GPS(binary.LittleEndian, utc.Format("2006:01:02"), utc.Hour(), utc.Minute(), float64(utc.Second()))},
			[]testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, at.Format("2006:01:02 15:04:05")), testimg.ASCII(exif.TagOffsetTimeOriginal, "+02:00")})
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("IMG_%04d.HEIC", i)), testimg.HEIF(phone, 0))
		canon := at.Add(-10*time.Minute - 4*time.Second).Format("2006:01:02 15:04:05")
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("CAN_%04d.JPG", i)), testimg.JPEG(testimg.Simple("Canon", "Canon EOS R5", canon)))
	}
	return dir
}

func TestSuggestFlow(t *testing.T) {
	dir := tripDir(t)
	code, out, errOut := run(t, "suggest", dir)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	apply := "camsync shift --camera 'Canon EOS R5' --by +10m4s " + shellQuote(dir)
	for _, want := range []string{"reference", "phone; GPS: clock exact", "estimated  +02:00 (guessed)  +10m4s  high", "8 photos within 1m of a reference photo", apply} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	code, out, _ = run(t, "suggest", "-o", "json", dir)
	var v struct {
		MaxOffset string `json:"maxOffset"`
		Cameras   []struct {
			Camera     string  `json:"camera"`
			Reference  bool    `json:"reference"`
			Basis      string  `json:"basis"`
			Shift      *string `json:"shift"`
			Confidence string  `json:"confidence"`
			GPS        *struct {
				Photos int    `json:"photos"`
				Error  string `json:"error"`
				Zone   string `json:"zone"`
			} `json:"gps"`
		} `json:"cameras"`
	}
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out)
	}
	if len(v.Cameras) != 2 || v.MaxOffset != "+12h" {
		t.Fatalf("json = %+v", v)
	}
	if c := v.Cameras[0]; c.Camera != "Apple iPhone 15" || !c.Reference || c.Basis != "phone" || c.Shift != nil || c.GPS == nil || c.GPS.Photos != 8 || c.GPS.Error != "none" || c.GPS.Zone != "+02:00" {
		t.Errorf("phone = %+v", c)
	}
	if c := v.Cameras[1]; c.Reference || c.Basis != "activity" || c.Shift == nil || *c.Shift != "+10m4s" || c.Confidence != "high" || c.GPS != nil {
		t.Errorf("canon = %+v", c)
	}

	// Applying the suggestion leaves nothing to do.
	if code, out, _ := run(t, "shift", "--camera", "Canon EOS R5", "--by", "+10m4s", dir); code != 0 {
		t.Fatal(out)
	}
	code, out, _ = run(t, "suggest", dir)
	if code != 0 || !strings.Contains(out, "estimated  +02:00 (guessed)  none") || strings.Contains(out, "To apply") {
		t.Errorf("after shift: %d\n%s", code, out)
	}

	// A named reference replaces the automatic choice, but a camera with GPS
	// times and a zone is still measured against GPS time.
	code, out, _ = run(t, "suggest", "--reference", "canon", dir)
	if code != 0 || !strings.Contains(out, "named with --reference") || !strings.Contains(out, "estimated  local       none   high        measured against GPS time in 8 photos") {
		t.Errorf("user reference: %d\n%s", code, out)
	}
}

func TestSuggestErrors(t *testing.T) {
	dir := setup(t)
	code, out, _ := run(t, "suggest", dir)
	if code != 0 || !strings.Contains(out, "No trusted clock found") || !strings.Contains(out, "no reference camera") {
		t.Errorf("no reference: %d\n%s", code, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"suggest", "--reference", "nikon", dir}, "no camera matches"},
		{[]string{"suggest", "--max-offset", "-1h", dir}, "--max-offset must be positive"},
		{[]string{"suggest", "--max-offset", "soon", dir}, "invalid duration"},
	} {
		if code, _, errOut := run(t, c.args...); code != ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %q", c.args, code, errOut)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"sony": "sony", "+10m4s": "+10m4s", "/a/b-c": "/a/b-c", "Canon EOS R5": "'Canon EOS R5'", "it's": `'it'\''s'`} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// zoneDir writes the same moments, around midnight in Berlin, from a phone
// recording +02:00, a camera on Japan time and a zone-less camera on UTC.
func zoneDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	start := time.Date(2024, 5, 1, 19, 0, 0, 0, time.UTC) // 21:00 in Berlin
	for i, gap := range []int{0, 1300, 700, 2900, 400, 3000, 1800, 900} {
		at := start.Add(time.Duration(i*1800+gap) * time.Second)
		write := func(name, mk, model string, clock time.Duration, tz string) {
			exifIFD := []testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, at.Add(clock).Format("2006:01:02 15:04:05"))}
			if tz != "" {
				exifIFD = append(exifIFD, testimg.ASCII(exif.TagOffsetTimeOriginal, tz))
			}
			tiff := testimg.BuildTIFF(binary.LittleEndian, []testimg.Entry{testimg.ASCII(exif.TagMake, mk), testimg.ASCII(exif.TagModel, model)}, exifIFD)
			mustWrite(t, filepath.Join(dir, fmt.Sprintf("%s_%04d.JPG", name, i)), testimg.JPEG(tiff))
		}
		write("IMG", "Apple", "iPhone 15", 2*time.Hour, "+02:00")
		write("CAN", "Canon", "Canon PowerShot G9 X", 9*time.Hour, "+09:00")
		write("DSC", "SONY", "ILCE-7M3", 0, "")
	}
	return dir
}

func TestLocalizeFlow(t *testing.T) {
	dir := zoneDir(t)
	code, out, errOut := run(t, "localize", "--dry-run", dir)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{
		"Apple iPhone 15       8      local             -           0        8        0",
		"Canon PowerShot G9 X  8      +09:00            +02:00      8        0        5",
		"SONY ILCE-7M3         8      +00:00 (guessed)  +02:00      8        0        3",
		"dry run: 16 sidecars written (16 new), 8 skipped (8 already local), 0 failed; local zone from 8 phone photos",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "CAN_0000.JPG.xmp")); err == nil {
		t.Fatal("dry run wrote a sidecar")
	}

	code, out, _ = run(t, "localize", "-v", "-c", "canon", dir)
	if code != 0 || !strings.Contains(out, "create  "+filepath.Join(dir, "CAN_0007.JPG")+": 2024-05-02 07:45:00 +09:00 -> 2024-05-02 00:45:00 +02:00") {
		t.Errorf("canon: %d\n%s", code, out)
	}
	code, out, _ = run(t, "localize", "-o", "json", dir)
	var v struct {
		Written, Unchanged int
		Cameras            []struct {
			Camera     string   `json:"camera"`
			LocalZones []string `json:"localZones"`
			ClockZone  *struct {
				Zone, Source string
			} `json:"clockZone"`
		} `json:"cameras"`
	}
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out)
	}
	if v.Written != 8 || v.Unchanged != 16 || v.Cameras[2].ClockZone == nil || v.Cameras[2].ClockZone.Zone != "+00:00" || v.Cameras[2].ClockZone.Source != "activity" {
		t.Errorf("json = %+v", v)
	}

	// Localized photos keep their moment: no offset, and suggest agrees.
	code, out, _ = run(t, "list", dir)
	if code != 0 || strings.Contains(out, "+7h") || strings.Contains(out, "mixed") {
		t.Errorf("list after localize: %d\n%s", code, out)
	}
	code, out, _ = run(t, "suggest", dir)
	if code != 0 || strings.Contains(out, "To apply") {
		t.Errorf("suggest after localize: %d\n%s", code, out)
	}

	// Without the phone the local zone must be given.
	for i := range 8 {
		if err := os.Remove(filepath.Join(dir, fmt.Sprintf("IMG_%04d.JPG", i))); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ = run(t, "reset", "-c", "canon", dir)
	if code != 0 {
		t.Fatal(out)
	}
	code, out, _ = run(t, "localize", "-n", "-c", "canon", dir)
	if code != 0 || !strings.Contains(out, "Canon PowerShot G9 X: 8 no local zone known within 1d; set one with --zone") {
		t.Errorf("no phone: %d\n%s", code, out)
	}
	code, out, _ = run(t, "localize", "-n", "-c", "canon", "--zone", "+02:00", dir)
	if code != 0 || !strings.Contains(out, "8 sidecars written") {
		t.Errorf("--zone: %d\n%s", code, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"localize", "--zone", "CEST", dir}, "invalid UTC offset"},
		{[]string{"localize", "--clock-zone", "sony", dir}, "must have the form CAMERA=ZONE"},
		{[]string{"localize", "--clock-zone", "nikon=Z", dir}, "no camera matches"},
		{[]string{"localize", "--zone-gap", "0", dir}, "--zone-gap must be positive"},
	} {
		if code, _, errOut := run(t, c.args...); code != ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %q", c.args, code, errOut)
		}
	}
}

// geoDir writes a phone walking north with GPS, a camera on Japan time
// photographing in between and once much later, and a GPS camera.
func geoDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	start := time.Date(2017, 8, 10, 8, 0, 0, 0, time.UTC)
	write := func(name, mk, model string, wall time.Time, tz string, gps *time.Time, lat float64) {
		ifd0 := []testimg.Entry{testimg.ASCII(exif.TagMake, mk), testimg.ASCII(exif.TagModel, model)}
		if gps != nil {
			ifd0 = append(ifd0, testimg.GPSAt(binary.LittleEndian, gps.Format("2006:01:02"), gps.Hour(), gps.Minute(), float64(gps.Second()), lat, 13.43))
		}
		exifIFD := []testimg.Entry{testimg.ASCII(exif.TagDateTimeOriginal, wall.Format("2006:01:02 15:04:05")), testimg.ASCII(exif.TagOffsetTimeOriginal, tz)}
		mustWrite(t, filepath.Join(dir, name), testimg.JPEG(testimg.BuildTIFF(binary.LittleEndian, ifd0, exifIFD)))
	}
	for i := range 4 {
		utc := start.Add(time.Duration(i*4) * time.Minute)
		write(fmt.Sprintf("IMG_%d.JPG", i), "Apple", "iPhone 15", utc.Add(2*time.Hour), "+02:00", &utc, 54.4+float64(i)*0.0036)
	}
	for _, m := range []int{2, 6, 10, 90} {
		write(fmt.Sprintf("CAN_%03d.JPG", m), "Canon", "Canon PowerShot G9 X", start.Add(time.Duration(m)*time.Minute+9*time.Hour), "+09:00", nil, 0)
	}
	gps := start.Add(5 * time.Minute)
	write("GPS_1.JPG", "Nikon", "Nikon Z 6", gps.Add(2*time.Hour), "+02:00", &gps, 54.5)
	return dir
}

func TestGeotagFlow(t *testing.T) {
	dir := geoDir(t)
	code, out, errOut := run(t, "geotag", "-n", dir)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{
		"Apple iPhone 15       4      4        0        0             4        -           -",
		"Canon PowerShot G9 X  4      0        3        2             1        2m          2m",
		"Nikon Z 6             1      1        0        0             1        -           -",
		"Canon PowerShot G9 X: 1 no located photo within 15m",
		"dry run: 3 sidecars written (3 new), 6 skipped (5 located, 0 already geotagged), 0 failed; positions from 5 located photos",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "CAN_002.JPG.xmp")); err == nil {
		t.Fatal("dry run wrote a sidecar")
	}

	code, out, _ = run(t, "geotag", "-v", "-c", "canon", dir)
	if code != 0 || !strings.Contains(out, "create  "+filepath.Join(dir, "CAN_002.JPG")+": 54.4018000, 13.4300000 (between IMG_0.JPG and IMG_1.JPG, 2m, 400m apart)  (CAN_002.JPG.xmp)") {
		t.Errorf("geotag: %d\n%s", code, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "CAN_002.JPG.xmp"))
	if f, _ := xmp.Read(data); f[xmp.GPSLatitude] != "54,24.108000N" || f[xmp.GPSLongitude] != "13,25.800000E" || f[xmp.GPSProcessingMethod] != "camsync" {
		t.Errorf("sidecar = %v", f)
	}

	code, out, _ = run(t, "geotag", "-o", "json", dir)
	var v struct {
		Written, Unchanged, Evidence int
		Cameras                      []struct {
			Camera  string `json:"camera"`
			Entries []struct {
				Path     string `json:"path"`
				Position *struct {
					Lat, Lon     float64
					Gap, From    string
					Interpolated bool
				} `json:"position"`
			} `json:"entries"`
		} `json:"cameras"`
	}
	if err := json.Unmarshal([]byte(out), &v); code != 0 || err != nil {
		t.Fatalf("%d %v\n%s", code, err, out)
	}
	if v.Written != 0 || v.Unchanged != 3 || v.Evidence != 5 {
		t.Errorf("second run = %+v", v)
	}

	// Localizing leaves the positions alone, reset removes them.
	if code, out, _ := run(t, "localize", dir); code != 0 {
		t.Fatal(out)
	}
	code, out, _ = run(t, "reset", "-c", "canon", dir)
	if code != 0 {
		t.Fatal(out)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "CAN_002.JPG.xmp"))
	if f, _ := xmp.Read(data); f[xmp.GPSLatitude] != "" || f[xmp.GPSProcessingMethod] != "" || f[xmp.DateTimeOriginal] != "2017-08-10T17:02:00+09:00" {
		t.Errorf("after reset: %v\n%s", f, data)
	}
	if code, out, _ := run(t, "geotag", "-n", "-c", "canon", dir); code != 0 || !strings.Contains(out, "3 sidecars written (0 new)") {
		t.Errorf("after reset: %d\n%s", code, out)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"geotag", "--max-gap", "-1m", dir}, "--max-gap must be positive"},
		{[]string{"geotag", "--max-bridge", "soon", dir}, "invalid duration"},
		{[]string{"geotag", "-c", "nikon z 7", dir}, "no camera matches"},
	} {
		if code, _, errOut := run(t, c.args...); code != ExitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code %d, stderr %q", c.args, code, errOut)
		}
	}
}

func libraryDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	write := func(rel, mk, model, dto string, pos *[2]float64) {
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
			write("Trips/Rügen/c"+string(rune('0'+d))+string(rune('a'+h))+".jpg", "Canon", "EOS R5", fmt.Sprintf("2024:05:%02d %02d:00:00", d, h), nil)
		}
	}
	write("Trips/Rügen/p1.jpg", "Apple", "iPhone 15", "2024:05:02 10:00:00", away)
	write("Inbox/p2.jpg", "Apple", "iPhone 15", "2024:05:01 11:30:00", away)
	write("Inbox/p3.jpg", "Apple", "iPhone 15", "2024:05:03 11:30:00", away)
	mustWrite(t, filepath.Join(root, "Inbox", "p3.jpg.xmp"), []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about=""/></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`))
	return root
}

func TestOrganizeReportPlanAndApply(t *testing.T) {
	root := libraryDir(t)
	code, out, errOut := run(t, "organize", root)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"HOME  ", "EVENTS", "FOLDERS", "Inbox", "inbox", "Trips/Rügen", "event", "SUGGESTIONS", "s1  move    2       high", "1 suggestions, 2 photos to move"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run(t, "organize", "-v", "--home", "52.52,13.40", "--layout", "periods", root)
	if code != 0 || !strings.Contains(out, "Inbox/p2.jpg") || strings.Contains(out, "inferred") {
		t.Errorf("verbose: code %d\n%s", code, out)
	}

	code, out, _ = run(t, "organize", "-o", "json", root)
	if code != 0 {
		t.Fatal(out)
	}
	var v struct {
		Root        string           `json:"root"`
		Suggestions []map[string]any `json:"suggestions"`
		Photos      []map[string]any `json:"photos"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Root != root || len(v.Suggestions) != 1 || len(v.Photos) != 15 {
		t.Errorf("json: %v %+v", err, v)
	}

	// Bad values are usage errors.
	for _, args := range [][]string{{"--gap", "x"}, {"--jump", "-1"}, {"--home", "far"}, {"--layout", "weekly"}, {"--min-trip-photos", "-2"}, {"--home-radius", "0"}} {
		if code, _, _ := run(t, append([]string{"organize"}, append(args, root)...)...); code != ExitUsage {
			t.Errorf("%v: code %d", args, code)
		}
	}

	// Write a plan, dry-run it, apply it.
	plan := filepath.Join(t.TempDir(), "plan.json")
	code, out, _ = run(t, "organize", "--plan", plan, root)
	if code != 0 || !strings.Contains(out, "Plan written to "+plan) {
		t.Fatalf("plan: %d\n%s", code, out)
	}
	data, err := os.ReadFile(plan)
	if err != nil || !strings.Contains(string(data), `"moves"`) || !strings.Contains(string(data), "p3.jpg.xmp") {
		t.Fatalf("plan file: %v %s", err, data)
	}
	code, out, _ = run(t, "organize", "--apply", plan, "--dry-run", "-v", root)
	if code != 0 || !strings.Contains(out, "dry run: 2 photos moved, 0 failed") || !strings.Contains(out, "move    ") {
		t.Errorf("dry run: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "Inbox", "p2.jpg")); err != nil {
		t.Error("dry run moved a file")
	}
	code, out, _ = run(t, "organize", "--apply", plan, "-o", "json")
	if code != 0 || !strings.Contains(out, `"moved": 2`) {
		t.Errorf("apply: %d\n%s", code, out)
	}
	for _, name := range []string{"p2.jpg", "p3.jpg", "p3.jpg.xmp"} {
		if _, err := os.Stat(filepath.Join(root, "Trips", "Rügen", name)); err != nil {
			t.Error(err)
		}
	}
	// Applying again fails: the files are gone from where the plan says.
	code, out, errOut = run(t, "organize", "--apply", plan)
	if code != ExitFailure || !strings.Contains(out, "FAILED") || !strings.Contains(errOut, "2 photos could not be moved") {
		t.Errorf("again: %d\n%s\n%s", code, out, errOut)
	}
	// A plan that is not JSON is a usage error, a missing one a failure.
	mustWrite(t, plan, []byte("nope"))
	if code, _, _ := run(t, "organize", "--apply", plan); code != ExitUsage {
		t.Errorf("bad plan: %d", code)
	}
	if code, _, _ := run(t, "organize", "--apply", filepath.Join(root, "missing.json")); code != ExitFailure {
		t.Errorf("missing plan: %d", code)
	}
}
