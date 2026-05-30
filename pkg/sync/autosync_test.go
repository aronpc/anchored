package sync

import (
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/memory"
)

func TestClassifyForAutoSync(t *testing.T) {
	root := "/tmp/project"

	t.Run("plain fact is syncable", func(t *testing.T) {
		_, ok := ClassifyForAutoSync(memory.Memory{
			ID: "1", Category: "fact",
			Content: "we run Go 1.22 on the ARM build server",
		}, root)
		if !ok {
			t.Fatal("a plain project fact should be syncable")
		}
	})

	t.Run("user-scoped never leaves the machine", func(t *testing.T) {
		_, ok := ClassifyForAutoSync(memory.Memory{
			ID: "2", Category: "fact", Content: "my personal todo",
			Metadata: map[string]any{"scope": "user"},
		}, root)
		if ok {
			t.Fatal("user-scoped memory must not be syncable")
		}
	})

	t.Run("blocked category never leaves the machine", func(t *testing.T) {
		_, ok := ClassifyForAutoSync(memory.Memory{
			ID: "3", Category: "preference", Content: "I prefer tabs",
		}, root)
		if ok {
			t.Fatal("preference category must not be syncable")
		}
	})

	t.Run("secret never leaves the machine", func(t *testing.T) {
		_, ok := ClassifyForAutoSync(memory.Memory{
			ID: "4", Category: "fact",
			Content: "the deploy key is AKIAIOSFODNN7EXAMPLE do not share",
		}, root)
		if ok {
			t.Fatal("memory containing a secret must not be syncable")
		}
	})

	t.Run("syncable content has no raw home path", func(t *testing.T) {
		content, ok := ClassifyForAutoSync(memory.Memory{
			ID: "5", Category: "fact",
			Content: "the config lives under the project root and loads on boot",
		}, root)
		if ok && strings.Contains(content, "/home/") {
			t.Fatal("returned content must not contain a raw home path")
		}
	})
}
