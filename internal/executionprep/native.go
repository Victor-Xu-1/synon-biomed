package executionprep

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// These adapters are embedded into the one backend artifact, not loaded from
// mutable task files or installed into scientific environments.
//
//go:embed python.py
var PythonParser string

//go:embed r.R
var RParser string

func DecodeNative(language string, raw []byte) ([]Fact, error) {
	if len(raw) > 4*MaxSourceBytes {
		return nil, errors.New("source fact output exceeds budget")
	}
	var facts []Fact
	if language == "python" || language == "powershell" {
		if err := json.Unmarshal(raw, &facts); err != nil {
			return nil, err
		}
	} else if language == "r" {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Split(line, "\t")
			if len(fields) < 2 {
				return nil, errors.New("invalid R source fact")
			}
			decoded := make([]string, len(fields)-1)
			for i, field := range fields[1:] {
				value, err := hex.DecodeString(field)
				if err != nil {
					return nil, err
				}
				decoded[i] = string(value)
			}
			facts = append(facts, Fact{Kind: fields[0], Name: decoded[0], Args: decoded[1:]})
		}
	} else {
		return nil, errors.New("unsupported native source parser")
	}
	if len(facts) > MaxFacts {
		return nil, errors.New("source fact count exceeds budget")
	}
	for _, fact := range facts {
		if len(fact.Args) > 256 || len(fact.Name) > 4096 {
			return nil, errors.New("invalid source fact")
		}
		switch fact.Kind {
		case "call", "source", "script", "process", "observation":
		default:
			return nil, errors.New("invalid source fact kind")
		}
	}
	return facts, nil
}
