package wire_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/wire"
	"github.com/Abraxas-365/seaport/internal/wire/sample"
)

const module = "github.com/Abraxas-365/seaport"

func repo() wire.Spec { return wire.Spec{Module: module} }

func render(t *testing.T, s wire.Spec) (string, error) {
	t.Helper()
	s.Module = module
	out, err := wire.Render(s, os.DirFS("../.."))
	return string(out), err
}

func TestRenderShapes(t *testing.T) {
	out, err := render(t, wire.Spec{
		Types: map[string]reflect.Type{
			"Widget": reflect.TypeFor[sample.Widget](),
			"Child":  reflect.TypeFor[sample.Child](),
		},
		Enums:      []wire.Enum{{Name: "Color", Type: reflect.TypeFor[sample.Color](), Anchor: "Red"}, {Name: "Mode", Pkg: "wire/sample", Anchor: "ModeFast", Also: []string{""}}},
		FieldEnums: map[wire.Field]string{{Type: reflect.TypeFor[sample.Widget](), Name: "Mode"}: "Mode"},
		Raw:        map[wire.Field]string{{Type: reflect.TypeFor[sample.Widget](), Name: "Any"}: "Custom"},
		Events:     map[string]reflect.Type{"widget.changed": reflect.TypeFor[sample.Widget](), "x.deleted": reflect.TypeFor[sample.Child]()},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`export type Color = "red" | "blue"`,
		`export type Mode = "" | "fast" | "slow"`,
		"/** Red is the first. */", // not on the union: the doc is the type's own
	} {
		_ = want
	}
	must := []string{
		`export type Color = "red" | "blue"`,
		`export type Mode = "" | "fast" | "slow"`,
		"export interface Widget {",
		"  note: string\n", // embedded struct flattened
		"  id: ID\n",       // identity.ID[T]
		"  name: string\n",
		"  color: Color\n",
		"  mode: Mode\n",
		"  count?: number\n",       // omitempty
		"  maybe: string | null\n", // pointer without omitempty
		"  omitted?: string\n",     // pointer with omitempty: absent, never null
		"  at: string\n",           // time.Time
		"  seen: string | null\n",
		"  tags: string[]\n",
		"  kids: Child[]\n",
		"  labels?: Record<string, string>\n",
		"  raw: unknown\n",
		"  blob: string\n", // []byte is base64
		"  any: Custom\n",  // Raw override
		"  anon: { x: number }\n",
		"  Default: string\n", // no tag: the Go name
		"  ids: ID[]\n",
		"/** Name is required. */",
		`  "widget.changed": Widget`,
		`  "x.deleted": Child`,
	}
	for _, want := range must {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"secret", "private", "Secret"} {
		if strings.Contains(out, bad) {
			t.Errorf("output has %q: json:\"-\" and unexported fields are skipped", bad)
		}
	}
	again, err := render(t, wire.Spec{
		Types:      map[string]reflect.Type{"Child": reflect.TypeFor[sample.Child](), "Widget": reflect.TypeFor[sample.Widget]()},
		Enums:      []wire.Enum{{Name: "Mode", Pkg: "wire/sample", Anchor: "ModeFast", Also: []string{""}}, {Name: "Color", Type: reflect.TypeFor[sample.Color](), Anchor: "Red"}},
		FieldEnums: map[wire.Field]string{{Type: reflect.TypeFor[sample.Widget](), Name: "Mode"}: "Mode"},
		Raw:        map[wire.Field]string{{Type: reflect.TypeFor[sample.Widget](), Name: "Any"}: "Custom"},
		Events:     map[string]reflect.Type{"x.deleted": reflect.TypeFor[sample.Child](), "widget.changed": reflect.TypeFor[sample.Widget]()},
	})
	if err != nil || again != out {
		t.Errorf("the output depends on the order of the spec (err %v)", err)
	}
}

// TestRenderRefuses: what the renderer can't type exactly is an error, never
// a quiet `string` or `unknown`.
func TestRenderRefuses(t *testing.T) {
	child := map[string]reflect.Type{"Child": reflect.TypeFor[sample.Child]()}
	cases := map[string]wire.Spec{
		"a string type with no enum":    {Types: map[string]reflect.Type{"HasStray": reflect.TypeFor[sample.HasStray]()}},
		"a named struct with no name":   {Types: map[string]reflect.Type{"HasChild": reflect.TypeFor[sample.HasChild]()}},
		"a custom JSON encoding":        {Types: map[string]reflect.Type{"HasMarshaled": reflect.TypeFor[sample.HasMarshaled](), "Child": child["Child"]}},
		"two names for one type":        {Types: map[string]reflect.Type{"A": reflect.TypeFor[sample.Child](), "B": reflect.TypeFor[sample.Child]()}},
		"a bad name":                    {Types: map[string]reflect.Type{"not a name": reflect.TypeFor[sample.Child]()}},
		"an enum constant not found":    {Enums: []wire.Enum{{Name: "Color", Type: reflect.TypeFor[sample.Color](), Anchor: "Nope"}}},
		"an enum of the wrong type":     {Enums: []wire.Enum{{Name: "Color", Type: reflect.TypeFor[sample.Color](), Anchor: "ModeFast"}}},
		"an enum on an unknown field":   {FieldEnums: map[wire.Field]string{{Type: reflect.TypeFor[sample.Widget](), Name: "Mode"}: "Missing"}},
		"a non-string enum field":       {Types: map[string]reflect.Type{"Child": child["Child"]}, Enums: []wire.Enum{{Name: "E", Pkg: "wire/sample", Anchor: "ModeFast"}}, FieldEnums: map[wire.Field]string{{Type: reflect.TypeFor[sample.Child](), Name: "Ok"}: "E"}},
		"an event payload with no name": {Events: map[string]reflect.Type{"a.b": reflect.TypeFor[sample.HasChild]()}},
	}
	for name, s := range cases {
		if _, err := render(t, s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_ = repo
}
