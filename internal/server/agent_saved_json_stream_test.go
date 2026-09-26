package server

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSavedJSONStreamSyntaxAndCursor(t *testing.T) {
	for _, body := range []string{"null", "[1,{\"key\":[true,false,null]}]", "{}\n ", "", "[1,]", "{}{}", "{\"k\":}", "{\"x\":null,\"x\":1}", "\"text\"", "[" + strings.Repeat("null,", 100000) + "null]"} {
		file, err := os.CreateTemp(t.TempDir(), "json-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(body); err != nil {
			t.Fatal(err)
		}
		actual := validateAgentSavedArtifactJSON(context.Background(), "data.json", file)
		want := validateAgentSavedArtifactJSONBytes("data.json", []byte(body))
		if (actual == nil) != (want == nil) {
			t.Fatalf("body prefix=%.60q result=%v expected=%v", body, actual, want)
		}
		if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != 0 {
			t.Fatalf("cursor=%d err=%v", position, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := validateAgentSavedArtifactJSON(ctx, "data.json", file); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
		file.Close()
	}
}
