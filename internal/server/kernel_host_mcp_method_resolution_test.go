package server

import "testing"

func TestKernelMCPMethodIsStrictSubset(t *testing.T) {
	tests := []struct {
		candidate string
		requested string
		want      bool
	}{
		{candidate: "pdb_get_entities", requested: "pdb_get_polymer_entities", want: true},
		{candidate: "pdb_get_entities", requested: "pdb_get_entities", want: false},
		{candidate: "pdb_get_structures", requested: "pdb_get_polymer_entities", want: false},
		{candidate: "delete_record", requested: "delete_old_archived_record_now", want: false},
		{candidate: "search", requested: "search_recent", want: false},
	}
	for _, test := range tests {
		if got := kernelMCPMethodIsStrictSubset(test.candidate, kernelMCPMethodTokens(test.requested)); got != test.want {
			t.Fatalf("candidate=%q requested=%q got=%t want=%t", test.candidate, test.requested, got, test.want)
		}
	}
}
