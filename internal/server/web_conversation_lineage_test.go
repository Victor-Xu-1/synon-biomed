package server

import (
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationFrameLabelPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		frame workspace.Frame
		want  string
	}{
		{
			name: "delegate name wins",
			frame: workspace.Frame{
				DelegateName: "HER2 resistance delegate",
				Name:         "Name", TaskSummary: "Summary",
			},
			want: "HER2 resistance delegate",
		},
		{
			name: "conversation name fallback",
			frame: workspace.Frame{
				Name: "ADC next-gen", TaskSummary: "Summary",
			},
			want: "ADC next-gen",
		},
		{
			name: "task summary fallback",
			frame: workspace.Frame{
				TaskSummary: "KRAS G12D inhibitor analysis",
			},
			want: "KRAS G12D inhibitor analysis",
		},
		{
			name: "input request fallback",
			frame: workspace.Frame{
				InputData: map[string]any{"request": "Search PDB structures"},
			},
			want: "Search PDB structures",
		},
		{
			name:  "untitled fallback is stable and non-empty",
			frame: workspace.Frame{},
			want:  webConversationUntitledLabel,
		},
		{
			name: "whitespace is trimmed",
			frame: workspace.Frame{
				Name: "  trimmed name  ",
			},
			want: "trimmed name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := webConversationFrameLabel(tc.frame); got != tc.want {
				t.Fatalf("webConversationFrameLabel()=%q want %q", got, tc.want)
			}
		})
	}
}
