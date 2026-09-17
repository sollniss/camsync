package xmp

import (
	"strings"
	"testing"
)

// bom replaces the {BOM} placeholder with a real byte order mark, which Go
// source files may not contain literally.
func bom(s string) string { return strings.ReplaceAll(s, "{BOM}", "\uFEFF") }

var lightroomXMP = "\uFEFF" + bom(`<?xpacket begin="{BOM}" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Adobe XMP Core 7.0-c000">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:exif="http://ns.adobe.com/exif/1.0/"
    xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
    xmlns:crs="http://ns.adobe.com/camera-raw-settings/1.0/"
    xmlns:dc="http://purl.org/dc/elements/1.1/"
   xmp:ModifyDate="2024-06-01T10:00:00+02:00"
   xmp:CreateDate="2024-05-01T12:34:56"
   exif:DateTimeOriginal="2024-05-01T12:34:56"
   exif:LensModel="RF24-70mm F2.8 L IS USM"
   tiff:Make="Canon"
   tiff:Model="Canon EOS R5"
   crs:Exposure2012="+0.50"
   crs:Temperature="5200">
   <dc:subject>
    <rdf:Bag>
     <rdf:li>trip &amp; travel</rdf:li>
    </rdf:Bag>
   </dc:subject>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>
`)

var exiftoolXMP = bom(`<?xpacket begin='{BOM}' id='W5M0MpCehiHzreSzNTczkc9d'?>
<x:xmpmeta xmlns:x='adobe:ns:meta/' x:xmptk='Image::ExifTool 12.76'>
<rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>
 <!-- a comment -->
 <rdf:Description rdf:about=''
  xmlns:ex='http://ns.adobe.com/exif/1.0/'>
  <ex:DateTimeOriginal>2024-05-01T12:34:56+02:00</ex:DateTimeOriginal>
  <ex:ISOSpeedRatings><rdf:Seq><rdf:li>100</rdf:li></rdf:Seq></ex:ISOSpeedRatings>
 </rdf:Description>
 <rdf:Description rdf:about=''
  xmlns:tiff='http://ns.adobe.com/tiff/1.0/'>
  <tiff:Model>ILCE-7M3</tiff:Model>
  <tiff:Make>SONY</tiff:Make>
 </rdf:Description>
</rdf:RDF>
</x:xmpmeta>
<?xpacket end='w'?>`)

func TestRead(t *testing.T) {
	f, err := Read([]byte(lightroomXMP))
	if err != nil {
		t.Fatal(err)
	}
	if f[DateTimeOriginal] != "2024-05-01T12:34:56" || f[CreateDate] != "2024-05-01T12:34:56" || f[Make] != "Canon" || f[Model] != "Canon EOS R5" {
		t.Errorf("lightroom fields = %v", f)
	}
	if f[Field{"http://ns.adobe.com/camera-raw-settings/1.0/", "Exposure2012"}] != "+0.50" {
		t.Errorf("unknown namespaces should be read too: %v", f)
	}
	if _, ok := f[Field{NSRDF, "about"}]; ok {
		t.Error("rdf:about should not be reported")
	}
	if _, ok := f[Field{"http://purl.org/dc/elements/1.1/", "subject"}]; ok {
		t.Error("structured values should be skipped")
	}
	f, err = Read([]byte(exiftoolXMP))
	if err != nil {
		t.Fatal(err)
	}
	if f[DateTimeOriginal] != "2024-05-01T12:34:56+02:00" || f[Make] != "SONY" || f[Model] != "ILCE-7M3" {
		t.Errorf("exiftool fields = %v", f)
	}
	if _, ok := f[CreateDate]; ok {
		t.Error("CreateDate should be absent")
	}
}

func TestRewriteAttributes(t *testing.T) {
	set := Fields{
		DateTimeOriginal:  "2024-05-01T12:37:26",
		DateTimeDigitized: "2024-05-01T12:37:27",
		CreateDate:        "2024-05-01T12:37:26",
		DateCreated:       "2024-05-01T12:37:26",
	}
	out, err := Rewrite([]byte(lightroomXMP), set)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF<?xpacket begin=\"\uFEFF\"") {
		t.Errorf("BOM / packet header lost: %q", s[:40])
	}
	for _, want := range []string{
		`exif:DateTimeOriginal="2024-05-01T12:37:26"`,
		`xmp:CreateDate="2024-05-01T12:37:26"`,
		`xmp:ModifyDate="2024-06-01T10:00:00+02:00"`, // untouched
		`crs:Exposure2012="+0.50"`,
		`<rdf:li>trip &amp; travel</rdf:li>`,
		`<exif:DateTimeDigitized>2024-05-01T12:37:27</exif:DateTimeDigitized>`,
		`<photoshop:DateCreated>2024-05-01T12:37:26</photoshop:DateCreated>`,
		`xmlns:photoshop='http://ns.adobe.com/photoshop/1.0/'`,
		`<?xpacket end="w"?>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %s\n%s", want, s)
		}
	}
	if strings.Count(s, "DateTimeOriginal") != 1 {
		t.Errorf("DateTimeOriginal duplicated:\n%s", s)
	}
	back, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		if back[k] != v {
			t.Errorf("read back %v = %q, want %q", k, back[k], v)
		}
	}
	if back[Make] != "Canon" {
		t.Errorf("Make lost")
	}
}

func TestRewriteElements(t *testing.T) {
	set := Fields{DateTimeOriginal: "2024-05-01T12:37:26+02:00", CreateDate: "2024-05-01T12:37:26+02:00"}
	out, err := Rewrite([]byte(exiftoolXMP), set)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`<ex:DateTimeOriginal>2024-05-01T12:37:26+02:00</ex:DateTimeOriginal>`,
		`<!-- a comment -->`,
		`<ex:ISOSpeedRatings><rdf:Seq><rdf:li>100</rdf:li></rdf:Seq></ex:ISOSpeedRatings>`,
		`<tiff:Model>ILCE-7M3</tiff:Model>`,
		`<xmp:CreateDate>2024-05-01T12:37:26+02:00</xmp:CreateDate>`,
		`<?xpacket end='w'?>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %s\n%s", want, s)
		}
	}
	back, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if back[DateTimeOriginal] != set[DateTimeOriginal] || back[CreateDate] != set[CreateDate] || back[Model] != "ILCE-7M3" {
		t.Errorf("read back = %v", back)
	}
}

func TestRewriteErrors(t *testing.T) {
	if _, err := Rewrite([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`), Fields{CreateDate: "x"}); err == nil {
		t.Error("expected error for document without rdf:RDF")
	}
	if _, err := Rewrite([]byte(`<broken`), Fields{CreateDate: "x"}); err == nil {
		t.Error("expected error for malformed XML")
	}
}

func TestNewRoundTrip(t *testing.T) {
	custom := Field{"http://example.com/custom/", "Thing"}
	set := Fields{DateTimeOriginal: "2024-05-01T12:37:26", Make: "A&B <Co>", Model: `X "1"`, custom: "yes"}
	out := New(set)
	back, err := Read(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for k, v := range set {
		if back[k] != v {
			t.Errorf("%v = %q, want %q", k, back[k], v)
		}
	}
	if !strings.Contains(string(out), `xmlns:ns1='http://example.com/custom/'`) {
		t.Errorf("unknown namespace needs a generated prefix:\n%s", out)
	}
}

func TestRewriteStrayEndTag(t *testing.T) {
	// RawToken does not verify that tags match; a stray closing tag must
	// produce an error, not a panic.
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="` + NSRDF + `"><rdf:Description/></rdf:RDF></x:xmpmeta></stray>`
	if _, err := Rewrite([]byte(doc), Fields{CreateDate: "x"}); err == nil {
		t.Error("expected error for stray closing tag")
	}
}

func TestUpdateRemoves(t *testing.T) {
	lat := Field{NSExif, "GPSLatitude"}
	lon := Field{NSExif, "GPSLongitude"}
	method := Field{NSExif, "GPSProcessingMethod"}
	withGPS, err := Rewrite([]byte(exiftoolXMP), Fields{lat: "54,25.5N", lon: "13,26.1E", method: "camsync"})
	if err != nil {
		t.Fatal(err)
	}
	// Attribute form, as Lightroom writes it.
	attrs := strings.Replace(lightroomXMP, `tiff:Make="Canon"`, `tiff:Make="Canon" exif:GPSLatitude="54,25.5N"`, 1)
	for name, doc := range map[string]string{"elements": string(withGPS), "attributes": attrs} {
		out, err := Update([]byte(doc), Fields{DateTimeOriginal: "2024-05-01T12:00:00"}, []Field{lat, lon, method, Make})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f, err := Read(out)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if _, ok := f[lat]; ok || f[lon] != "" || f[method] != "" || f[Make] != "" || f[DateTimeOriginal] != "2024-05-01T12:00:00" {
			t.Errorf("%s: fields = %v\n%s", name, f, out)
		}
		if name == "elements" && !strings.Contains(string(out), "<ex:ISOSpeedRatings><rdf:Seq><rdf:li>100</rdf:li></rdf:Seq></ex:ISOSpeedRatings>") {
			t.Errorf("unrelated content lost:\n%s", out)
		}
	}
	// Set wins over remove.
	out, _ := Update([]byte(exiftoolXMP), Fields{Model: "X"}, []Field{Model})
	if f, _ := Read(out); f[Model] != "X" {
		t.Errorf("set and removed: %v", f)
	}
}
