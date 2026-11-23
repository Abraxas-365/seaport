package ship

import "testing"

func TestMatchOption(t *testing.T) {
	opts := []string{"Aprobar", "Rechazar", "Posponer"}
	for _, tc := range []struct {
		reply string
		want  string
		ok    bool
	}{
		{"1", "Aprobar", true},
		{" 2 ", "Rechazar", true},
		{"3.", "Posponer", true},
		{"opción 2", "Rechazar", true},
		{"Option 1", "Aprobar", true},
		{"aprobar", "Aprobar", true},
		{"APROBAR!", "Aprobar", true},
		{"@bot 2", "Rechazar", true},
		{"4", "", false},
		{"0", "", false},
		{"12", "", false},
		{"2 pero espera", "", false},
		{"sí", "", false},
		{"", "", false},
		{"+1", "", false},
	} {
		got, ok := MatchOption(opts, tc.reply)
		if got != tc.want || ok != tc.ok {
			t.Errorf("MatchOption(%q) = %q, %v; want %q, %v", tc.reply, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateOptions(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []string
		ok   bool
	}{
		"two":        {[]string{"Sí", "No"}, true},
		"six":        {[]string{"a", "b", "c", "d", "e", "f"}, true},
		"one":        {[]string{"Sí"}, false},
		"seven":      {[]string{"a", "b", "c", "d", "e", "f", "g"}, false},
		"duplicate":  {[]string{"Sí", "sí"}, false},
		"digit":      {[]string{"1 vez", "No"}, false},
		"empty":      {[]string{"Sí", " "}, false},
		"multi-line": {[]string{"Sí", "No\nsé"}, false},
		"long":       {[]string{"Sí", string(make([]byte, 65))}, false},
	} {
		if err := ValidateOptions(tc.opts); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestApprovalCard(t *testing.T) {
	got := ApprovalCard(" ¿Despliego? ", []string{"Sí", "No"})
	want := "¿Despliego?\n\n1. Sí\n2. No\n" + ApprovalTrailer
	if got != want {
		t.Errorf("card:\n%s\nwant:\n%s", got, want)
	}
}

func TestFromSeaport(t *testing.T) {
	for _, id := range []string{SystemSchedule, SystemApproval, SystemBrowser} {
		if !FromSeaport(id) {
			t.Errorf("%q is Seaport's own", id)
		}
	}
	// A channel's own system sender (Datadog's alerts) comes from outside.
	for _, id := range []string{"", "system:datadog", "cloudwatch", "+34600000000", "system:schedule2"} {
		if FromSeaport(id) {
			t.Errorf("%q is not Seaport's own", id)
		}
	}
}
