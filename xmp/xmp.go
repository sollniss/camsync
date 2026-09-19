// Package xmp reads and updates XMP sidecar files.
//
// The package deliberately does not model RDF in full. Read collects the
// simple properties of every rdf:Description, whether written as attributes
// or as child elements with text content. Update replaces or adds
// properties and leaves everything else in the document intact: unrelated
// elements, attributes, comments and processing instructions are written back
// unchanged in content, so sidecars written by other tools keep their data.
// The serialisation is normalised, not byte-identical: attributes are quoted
// with double quotes and character references are resolved.
package xmp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"maps"
	"sort"
	"strconv"
	"strings"
)

// Namespace URIs of the properties this package has names for.
const (
	NSRDF       = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	NSXMPMeta   = "adobe:ns:meta/"
	NSExif      = "http://ns.adobe.com/exif/1.0/"
	NSXMP       = "http://ns.adobe.com/xap/1.0/"
	NSPhotoshop = "http://ns.adobe.com/photoshop/1.0/"
	NSTIFF      = "http://ns.adobe.com/tiff/1.0/"
)

// Field identifies an XMP property by namespace URI and local name.
type Field struct{ NS, Local string }

// Commonly used properties.
var (
	DateTimeOriginal  = Field{NSExif, "DateTimeOriginal"}
	DateTimeDigitized = Field{NSExif, "DateTimeDigitized"}
	CreateDate        = Field{NSXMP, "CreateDate"}
	ModifyDate        = Field{NSXMP, "ModifyDate"}
	DateCreated       = Field{NSPhotoshop, "DateCreated"}
	Make              = Field{NSTIFF, "Make"}
	Model             = Field{NSTIFF, "Model"}

	// GPS coordinates in the XMP form "DDD,MM.mmmmk", k being N, S, E or W.
	GPSLatitude  = Field{NSExif, "GPSLatitude"}
	GPSLongitude = Field{NSExif, "GPSLongitude"}
	// GPSProcessingMethod names how a position was found; camsync marks
	// its estimates with it.
	GPSProcessingMethod = Field{NSExif, "GPSProcessingMethod"}

	rdfRDF         = Field{NSRDF, "RDF"}
	rdfDescription = Field{NSRDF, "Description"}
)

// knownPrefixes maps namespace URIs to the prefixes used when this package has
// to declare a namespace itself. Namespaces missing here get a generated
// prefix (ns1, ns2, ...). The map is never modified after initialisation.
var knownPrefixes = map[string]string{
	NSExif:      "exif",
	NSXMP:       "xmp",
	NSPhotoshop: "photoshop",
	NSTIFF:      "tiff",
}

// Fields maps properties to their string values.
type Fields map[Field]string

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func tokenize(data []byte) ([]xml.Token, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var toks []xml.Token
	for {
		t, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return toks, nil
		}
		if err != nil {
			return nil, err
		}
		toks = append(toks, xml.CopyToken(t))
	}
}

// scope maps namespace prefixes to URIs; "" is the default namespace.
type scope map[string]string

type nsTracker struct{ stack []scope }

func (n *nsTracker) push(se xml.StartElement) scope {
	s := scope{}
	if len(n.stack) > 0 {
		maps.Copy(s, n.stack[len(n.stack)-1])
	}
	for _, a := range se.Attr {
		if a.Name.Space == "xmlns" {
			s[a.Name.Local] = a.Value
		} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
			s[""] = a.Value
		}
	}
	n.stack = append(n.stack, s)
	return s
}

func (n *nsTracker) pop() {
	if len(n.stack) > 0 {
		n.stack = n.stack[:len(n.stack)-1]
	}
}

func (s scope) element(name xml.Name) Field { return Field{s[name.Space], name.Local} }

// attribute resolves an attribute name. Unprefixed attributes have no
// namespace and namespace declarations are not properties, so both are
// reported as not ok.
func (s scope) attribute(name xml.Name) (Field, bool) {
	if name.Space == "" || name.Space == "xmlns" || name.Space == "xml" {
		return Field{}, false
	}
	return Field{s[name.Space], name.Local}, true
}

// Read extracts the simple properties of all rdf:Description elements in
// an XMP document. Properties with structured values (rdf:Seq, rdf:Alt and
// the like) are skipped; rdf:about and other RDF attributes are not
// reported.
func Read(data []byte) (Fields, error) {
	toks, err := tokenize(bytes.TrimPrefix(data, utf8BOM))
	if err != nil {
		return nil, err
	}
	out := Fields{}
	var tr nsTracker
	var names []Field
	var capture *Field
	var buf strings.Builder
	depth, nested := 0, false
	for _, t := range toks {
		switch t := t.(type) {
		case xml.StartElement:
			s := tr.push(t)
			name := s.element(t.Name)
			parent := Field{}
			if len(names) > 0 {
				parent = names[len(names)-1]
			}
			names = append(names, name)
			if capture != nil {
				depth++
				nested = true
				continue
			}
			switch {
			case name == rdfDescription:
				for _, a := range t.Attr {
					if f, ok := s.attribute(a.Name); ok && f.NS != NSRDF {
						out[f] = a.Value
					}
				}
			case parent == rdfDescription && name.NS != NSRDF:
				capture = &name
				buf.Reset()
				depth, nested = 0, false
			}
		case xml.EndElement:
			tr.pop()
			if len(names) > 0 {
				names = names[:len(names)-1]
			}
			if capture != nil {
				if depth > 0 {
					depth--
				} else {
					if !nested {
						out[*capture] = strings.TrimSpace(buf.String())
					}
					capture = nil
				}
			}
		case xml.CharData:
			if capture != nil && depth == 0 {
				buf.Write(t)
			}
		}
	}
	return out, nil
}

// Update returns data with the given fields set, the properties listed in
// remove taken out and nothing else changed in content (see the package
// comment for how the output is serialised). Fields already present (as
// attribute or element) are replaced in place; missing ones are added in a
// new rdf:Description at the end of rdf:RDF. Removal covers every
// rdf:Description, whether the property is written as attribute or as
// element. A field both set and removed is set.
func Update(data []byte, set Fields, remove []Field) ([]byte, error) {
	bom := bytes.HasPrefix(data, utf8BOM)
	toks, err := tokenize(bytes.TrimPrefix(data, utf8BOM))
	if err != nil {
		return nil, err
	}
	remaining := maps.Clone(set)
	removing := map[Field]bool{}
	for _, f := range remove {
		if _, ok := set[f]; !ok {
			removing[f] = true
		}
	}
	var out bytes.Buffer
	if bom {
		out.Write(utf8BOM)
	}
	var tr nsTracker
	var names []Field
	var capture *Field
	depth, nested := 0, false
	skipEnd := false
	skip := 0 // depth inside a removed element
	for i, t := range toks {
		if skip > 0 {
			switch t.(type) {
			case xml.StartElement, xml.EndElement:
			default:
				continue // content of a removed element
			}
		}
		switch t := t.(type) {
		case xml.StartElement:
			s := tr.push(t)
			name := s.element(t.Name)
			parent := Field{}
			if len(names) > 0 {
				parent = names[len(names)-1]
			}
			names = append(names, name)
			if skip > 0 || (capture == nil && parent == rdfDescription && removing[name]) {
				skip++
				continue
			}
			if capture != nil {
				depth++
				nested = true
				writeStart(&out, t, false)
				continue
			}
			capturing := false
			switch name {
			case rdfDescription:
				attrs := make([]xml.Attr, 0, len(t.Attr))
				for _, a := range t.Attr {
					if f, ok := s.attribute(a.Name); ok {
						if removing[f] {
							continue
						}
						if v, ok := remaining[f]; ok {
							a.Value = v
							delete(remaining, f)
						}
					}
					attrs = append(attrs, a)
				}
				t.Attr = attrs
			default:
				if _, ok := remaining[name]; ok {
					capture = &name
					depth, nested = 0, false
					capturing = true
				}
			}
			selfClose := false
			if !capturing && i+1 < len(toks) {
				if e, ok := toks[i+1].(xml.EndElement); ok && e.Name == t.Name {
					selfClose = true
				}
			}
			writeStart(&out, t, selfClose)
			skipEnd = selfClose
		case xml.EndElement:
			// RawToken does not check that tags match, so a stray end tag
			// would leave nothing to pop.
			if len(names) == 0 {
				return nil, errors.New("xmp: unexpected closing tag </" + t.Name.Local + ">")
			}
			name := names[len(names)-1]
			names = names[:len(names)-1]
			tr.pop()
			if skip > 0 {
				skip--
				continue
			}
			if capture != nil {
				if depth > 0 {
					depth--
				} else {
					if !nested {
						out.WriteString(escapeText(remaining[*capture]))
						delete(remaining, *capture)
					}
					capture = nil
				}
			}
			if name == rdfRDF && len(remaining) > 0 {
				writeDescription(&out, t.Name.Space, remaining)
				remaining = Fields{}
			}
			if skipEnd {
				skipEnd = false
			} else {
				out.WriteString("</")
				writeName(&out, t.Name.Space, t.Name.Local)
				out.WriteByte('>')
			}
		case xml.CharData:
			if capture != nil && depth == 0 && !nested {
				continue // replaced by the new value
			}
			out.WriteString(escapeText(string(t)))
		case xml.Comment:
			out.WriteString("<!--")
			out.Write(t)
			out.WriteString("-->")
		case xml.ProcInst:
			out.WriteString("<?")
			out.WriteString(t.Target)
			out.WriteByte(' ')
			out.Write(t.Inst)
			out.WriteString("?>")
		case xml.Directive:
			out.WriteString("<!")
			out.Write(t)
			out.WriteByte('>')
		}
	}
	if len(remaining) > 0 {
		return nil, errors.New("xmp: no rdf:RDF element found")
	}
	return out.Bytes(), nil
}

// writeName writes a qualified name as prefix:local, or just local when
// prefix is empty.
func writeName(out *bytes.Buffer, prefix, local string) {
	if prefix != "" {
		out.WriteString(prefix)
		out.WriteByte(':')
	}
	out.WriteString(local)
}

func writeStart(out *bytes.Buffer, t xml.StartElement, selfClose bool) {
	out.WriteByte('<')
	writeName(out, t.Name.Space, t.Name.Local)
	for _, a := range t.Attr {
		writeAttr(out, " ", a.Name.Space, a.Name.Local, a.Value, '"')
	}
	if selfClose {
		out.WriteString("/>")
	} else {
		out.WriteByte('>')
	}
}

// writeAttr writes lead (whitespace or newline plus indent) followed by
// prefix:local=value, quoted with quote and escaped. Existing attributes are
// rewritten with double quotes; generated content uses single quotes like
// exiftool does.
func writeAttr(out *bytes.Buffer, lead, prefix, local, value string, quote byte) {
	out.WriteString(lead)
	writeName(out, prefix, local)
	out.WriteByte('=')
	out.WriteByte(quote)
	out.WriteString(escapeAttr(value))
	out.WriteByte(quote)
}

// writeDescription appends rdf:Description elements holding the given
// fields: one per namespace, with the properties as child elements. This is
// the layout exiftool writes (and therefore what Immich and other
// exiftool-based readers see every day), so it is the safest choice.
func writeDescription(out *bytes.Buffer, rdfPrefix string, fields Fields) {
	declare := rdfPrefix == ""
	if declare {
		rdfPrefix = "rdf"
	}
	px := prefixes(fields)
	sorted := sortedFields(fields, px)
	for _, ns := range sortedNamespaces(px) {
		out.WriteString(" <")
		out.WriteString(rdfPrefix)
		out.WriteString(":Description")
		if declare {
			writeAttr(out, " ", "xmlns", rdfPrefix, NSRDF, '\'')
		}
		out.WriteByte(' ')
		out.WriteString(rdfPrefix)
		out.WriteString(":about=''")
		writeAttr(out, "\n  ", "xmlns", px[ns], ns, '\'')
		out.WriteByte('>')
		for _, f := range sorted {
			if f.NS != ns {
				continue
			}
			out.WriteString("\n  <")
			writeName(out, px[ns], f.Local)
			out.WriteByte('>')
			out.WriteString(escapeText(fields[f]))
			out.WriteString("</")
			writeName(out, px[ns], f.Local)
			out.WriteByte('>')
		}
		out.WriteString("\n </")
		out.WriteString(rdfPrefix)
		out.WriteString(":Description>\n")
	}
}

// New builds a sidecar document containing the given fields, in the same
// layout exiftool produces.
func New(fields Fields) []byte {
	var b bytes.Buffer
	b.WriteString("<?xpacket begin='\xEF\xBB\xBF' id='W5M0MpCehiHzreSzNTczkc9d'?>\n")
	b.WriteString(`<x:xmpmeta xmlns:x='` + NSXMPMeta + `' x:xmptk='camsync'>` + "\n")
	b.WriteString(`<rdf:RDF xmlns:rdf='` + NSRDF + `'>` + "\n")
	writeDescription(&b, "rdf", fields)
	b.WriteString("</rdf:RDF>\n</x:xmpmeta>\n<?xpacket end='w'?>\n")
	return b.Bytes()
}

// prefixes returns a prefix for every namespace used in fields, taking
// registered ones from knownPrefixes and generating ns1, ns2, ... for the rest.
func prefixes(fields Fields) map[string]string {
	px := map[string]string{}
	var unknown []string
	for f := range fields {
		if _, done := px[f.NS]; done {
			continue
		}
		if p, ok := knownPrefixes[f.NS]; ok {
			px[f.NS] = p
		} else {
			px[f.NS] = ""
			unknown = append(unknown, f.NS)
		}
	}
	sort.Strings(unknown)
	for i, ns := range unknown {
		px[ns] = "ns" + strconv.Itoa(i+1)
	}
	return px
}

func sortedFields(fields Fields, px map[string]string) []Field {
	fs := make([]Field, 0, len(fields))
	for f := range fields {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].NS != fs[j].NS {
			return px[fs[i].NS] < px[fs[j].NS]
		}
		return fs[i].Local < fs[j].Local
	})
	return fs
}

func sortedNamespaces(px map[string]string) []string {
	out := make([]string, 0, len(px))
	for ns := range px {
		out = append(out, ns)
	}
	sort.Slice(out, func(i, j int) bool { return px[out[i]] < px[out[j]] })
	return out
}

var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
)

func escapeText(s string) string { return textEscaper.Replace(s) }
func escapeAttr(s string) string { return attrEscaper.Replace(s) }
