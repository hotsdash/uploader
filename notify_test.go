package main

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// Text from the dashboard only ever goes into the toast as text: it can't add elements or attributes.
func TestToastTextStaysText(t *testing.T) {
	for _, p := range []string{
		`</text></binding></visual><actions><action content="x" arguments="calc:" activationType="protocol"/></actions>`,
		`" activationType="protocol" launch="ms-msdt:x`,
		`'><toast launch='file:///c:/x.exe'>`,
		`&lt;already&gt; &amp; ]]> <!-- <?x ?> <![CDATA[`,
		"curly ’quotes‘ $(x) `y` é 🎮",
	} {
		x := toastXML(p, p+"\n"+p, "https://hots.example.com/matches/1")
		var elements, attributes, texts []string
		d := xml.NewDecoder(strings.NewReader(x))
		for {
			tok, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%q: not XML: %v", p, err)
			}
			switch v := tok.(type) {
			case xml.StartElement:
				elements = append(elements, v.Name.Local)
				for _, a := range v.Attr {
					attributes = append(attributes, a.Name.Local+"="+a.Value)
				}
			case xml.CharData:
				texts = append(texts, string(v))
			case xml.Comment, xml.ProcInst, xml.Directive:
				t.Fatalf("%q: added %T", p, v)
			}
		}
		if got := strings.Join(elements, " "); got != "toast visual binding text text text" {
			t.Fatalf("%q: elements %s", p, got)
		}
		if got := strings.Join(attributes, " "); got != "activationType=protocol launch=https://hots.example.com/matches/1 template=ToastGeneric" {
			t.Fatalf("%q: attributes %s", p, got)
		}
		if len(texts) != 3 || texts[0] != p || texts[1] != p || texts[2] != p {
			t.Fatalf("%q: text came back as %q", p, texts)
		}
	}
}

func TestToastXML(t *testing.T) {
	x := toastXML(`A & B <win>`, "one\n\ntwo\x07", "https://hots.example.com/live")
	want := `<toast activationType="protocol" launch="https://hots.example.com/live"><visual><binding template="ToastGeneric"><text>A &amp; B &lt;win&gt;</text><text>one</text><text>two</text></binding></visual></toast>`
	if x != want {
		t.Fatalf("got  %s\nwant %s", x, want)
	}
	if strings.Contains(toastXML("t", "m", `\\evil.example\share\x.exe`), "launch=") {
		t.Fatal("only web addresses open from a notification")
	}
}

func TestLongToastsAreClipped(t *testing.T) {
	x := toastXML(strings.Repeat("t", 5000), strings.Repeat("m", 50000), "")
	if strings.Count(x, "t") > 300 || strings.Count(x, "m") > 1600 {
		t.Fatalf("toast is %d bytes", len(x))
	}
}
