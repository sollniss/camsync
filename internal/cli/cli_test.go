package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
