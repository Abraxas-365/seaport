package ship_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

func TestOutboundFiles(t *testing.T) {
	conv := identity.NewConversationID()
	file := func(name string, n int) ship.File { return ship.File{Name: name, Data: bytes.Repeat([]byte{'x'}, n)} }
	ok := []ship.Outbound{
		{Conversation: conv, Text: "hi"},
		{Conversation: conv, Files: []ship.File{file("report.md", 10)}},
		{Conversation: conv, Text: "see", Files: []ship.File{file("QA report (v2).md", 10), file("shot-1.png", 10)}},
	}
	for _, o := range ok {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	many := make([]ship.File, ship.MaxAttachments+1)
	for i := range many {
		many[i] = file("f"+strings.Repeat("a", i)+".txt", 1)
	}
	bad := map[string]ship.Outbound{
		"no text, no files": {Conversation: conv},
		"path":              {Conversation: conv, Files: []ship.File{file("../x.md", 1)}},
		"dir":               {Conversation: conv, Files: []ship.File{file("a/b.md", 1)}},
		"hidden":            {Conversation: conv, Files: []ship.File{file(".env", 1)}},
		"dots":              {Conversation: conv, Files: []ship.File{file("a..b", 1)}},
		"empty":             {Conversation: conv, Files: []ship.File{file("a.md", 0)}},
		"too big":           {Conversation: conv, Files: []ship.File{file("a.bin", ship.MaxFile+1)}},
		"too big together":  {Conversation: conv, Files: []ship.File{file("a.bin", ship.MaxFile), file("b.bin", ship.MaxFile), file("c.bin", ship.MaxFile)}},
		"same name":         {Conversation: conv, Files: []ship.File{file("a.md", 1), file("A.md", 1)}},
		"too many":          {Conversation: conv, Files: many},
	}
	for name, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
