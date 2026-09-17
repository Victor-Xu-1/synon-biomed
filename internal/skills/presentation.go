package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const presentationFilename = "catalog-ui.json"
const maxPresentationBytes = 1 << 20

// Presentation is adjacent to executable Skill contracts and never affects
// discovery, authorization or scientific execution. Catalogs are scoped to
// their directory subtree; a nested catalog owns its own descendants.
type presentationCatalog struct {
	SchemaVersion int                             `json:"schema_version"`
	Skills        map[string]presentationMetadata `json:"skills"`
	seen          map[string]bool
	path          string
}

type presentationMetadata struct {
	Category        string            `json:"category"`
	DescriptionI18n map[string]string `json:"description_i18n"`
}

type presentationLoader struct {
	root    *os.Root
	absRoot string
	catalog *Catalog
	byDir   map[string]*presentationCatalog
}

func newPresentationLoader(root *os.Root, absRoot string, catalog *Catalog) *presentationLoader {
	return &presentationLoader{root: root, absRoot: absRoot, catalog: catalog, byDir: map[string]*presentationCatalog{}}
}

func (p *presentationLoader) apply(skill *Skill, relative string) {
	for dir := filepath.Dir(relative); ; dir = filepath.Dir(dir) {
		metadata, checked := p.byDir[dir]
		if !checked {
			var err error
			metadata, err = readPresentationCatalog(p.root, dir)
			if err != nil {
				p.catalog.AddLoadError(filepath.Join(p.absRoot, dir, presentationFilename), err)
				// A malformed nearest catalog must not inherit another catalog's
				// labels, but its Skills remain available without presentation.
				metadata = &presentationCatalog{}
			}
			p.byDir[dir] = metadata
		}
		if metadata != nil {
			if metadata.Skills == nil {
				return
			}
			entry, ok := metadata.Skills[skill.Name]
			if !ok {
				p.catalog.AddLoadError(skill.Path, fmt.Errorf("skill %q is missing from %s", skill.Name, metadata.path))
				return
			}
			metadata.seen[skill.Name] = true
			skill.Category = entry.Category
			skill.DescriptionI18n = make(map[string]string, len(entry.DescriptionI18n))
			for locale, description := range entry.DescriptionI18n {
				skill.DescriptionI18n[locale] = description
			}
			return
		}
		if dir == "." {
			return
		}
	}
}

func (p *presentationLoader) finish() {
	for _, metadata := range p.byDir {
		if metadata == nil {
			continue
		}
		for name := range metadata.Skills {
			if !metadata.seen[name] {
				p.catalog.AddLoadError(filepath.Join(p.absRoot, metadata.path), fmt.Errorf("presentation metadata references missing skill %q", name))
			}
		}
	}
}

func readPresentationCatalog(root *os.Root, dir string) (*presentationCatalog, error) {
	path := filepath.Join(dir, presentationFilename)
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPresentationBytes {
		return nil, errors.New("presentation catalog must be a regular file within the metadata size limit")
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, errors.New("presentation catalog changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPresentationBytes+1))
	if err != nil || len(raw) > maxPresentationBytes {
		return nil, errors.New("presentation catalog could not be read within the metadata size limit")
	}
	if err := validatePresentationJSONKeys(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return nil, err
	}
	var catalog presentationCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode presentation catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("presentation catalog contains trailing JSON")
	}
	if catalog.SchemaVersion != 1 || len(catalog.Skills) == 0 {
		return nil, errors.New("presentation catalog requires schema_version 1 and non-empty skills")
	}
	for name, entry := range catalog.Skills {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\\\x00\r\n") {
			return nil, errors.New("presentation catalog contains an invalid skill name")
		}
		if entry.Category == "" || entry.Category != strings.TrimSpace(entry.Category) || len(entry.Category) > 96 || strings.ContainsAny(entry.Category, "\x00\r\n") {
			return nil, fmt.Errorf("presentation skill %q has an invalid category", name)
		}
		if len(entry.DescriptionI18n) == 0 {
			return nil, fmt.Errorf("presentation skill %q has no localized description", name)
		}
		for locale, description := range entry.DescriptionI18n {
			if strings.TrimSpace(locale) == "" || locale != strings.TrimSpace(locale) || strings.TrimSpace(description) == "" || len([]rune(description)) > 1000 || strings.ContainsAny(locale+description, "\x00") {
				return nil, fmt.Errorf("presentation skill %q has an invalid localized description", name)
			}
		}
	}
	catalog.path, catalog.seen = path, map[string]bool{}
	return &catalog, nil
}

// encoding/json accepts duplicate object keys. Reject them so reviewable
// metadata cannot silently overwrite an earlier declaration.
func validatePresentationJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("presentation JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("presentation JSON contains duplicate or invalid key %q", key)
			}
			seen[name] = true
		}
		if err := validatePresentationJSONKeys(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
