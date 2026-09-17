package productidentity

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const authoritySchema = "synon.product-identity.v1"

var (
	semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	machineSlugPattern     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

//go:embed product-identity.json
var embeddedAuthority []byte

type Identity struct {
	Schema      string
	DisplayName string
	Version     string
	MachineSlug string
}

var current = mustDecode(embeddedAuthority)

func Current() Identity {
	return current
}

func (identity Identity) FullDisplay() string {
	return identity.DisplayName + " v" + identity.Version
}

func (identity Identity) SourcePackage() string {
	return identity.MachineSlug + "-v" + identity.Version
}

func (identity Identity) UserAgent() string {
	return identity.MachineSlug + "/" + identity.Version
}

func mustDecode(raw []byte) Identity {
	identity, err := decode(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid embedded product identity: %v", err))
	}
	return identity
}

func decode(raw []byte) (Identity, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Identity{}, errors.New("authority must be a JSON object")
	}
	fields := make(map[string]json.RawMessage, 4)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return Identity{}, errors.New("authority key is invalid")
		}
		key, ok := keyToken.(string)
		if !ok {
			return Identity{}, errors.New("authority key is not a string")
		}
		if _, exists := fields[key]; exists {
			return Identity{}, errors.New("authority contains a duplicate key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return Identity{}, errors.New("authority value is invalid")
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Identity{}, errors.New("authority object is incomplete")
	}
	if token, err = decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return Identity{}, errors.New("authority contains trailing data")
	}
	if len(fields) != 4 {
		return Identity{}, errors.New("authority fields are incomplete")
	}
	readString := func(key string) (string, error) {
		value, exists := fields[key]
		if !exists {
			return "", errors.New("authority field is missing")
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return "", errors.New("authority field is not a string")
		}
		return decoded, nil
	}
	schema, err := readString("schema")
	if err != nil {
		return Identity{}, err
	}
	displayName, err := readString("display_name")
	if err != nil {
		return Identity{}, err
	}
	version, err := readString("version")
	if err != nil {
		return Identity{}, err
	}
	machineSlug, err := readString("machine_slug")
	if err != nil {
		return Identity{}, err
	}
	if schema != authoritySchema {
		return Identity{}, errors.New("authority schema is unsupported")
	}
	if displayName == "" || strings.TrimSpace(displayName) != displayName || len(displayName) > 80 {
		return Identity{}, errors.New("display name is invalid")
	}
	if !semanticVersionPattern.MatchString(version) {
		return Identity{}, errors.New("version is invalid")
	}
	if !machineSlugPattern.MatchString(machineSlug) {
		return Identity{}, errors.New("machine slug is invalid")
	}
	return Identity{Schema: schema, DisplayName: displayName, Version: version, MachineSlug: machineSlug}, nil
}
