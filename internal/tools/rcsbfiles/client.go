package rcsbfiles

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/tools/securefetch"
)

const (
	productionBaseURL = "https://files.rcsb.org"
	probeBytes        = 4096
)

type ResourceKind string

const (
	EntryCoordinates ResourceKind = "entry"
	LigandDefinition ResourceKind = "ligand"
)

type Format string

const (
	FormatPDB Format = "pdb"
	FormatCIF Format = "cif"
)

type Input struct {
	ResourceKind ResourceKind
	EntryID      string
	ComponentID  string
	Format       Format
	Filename     string
}

type Result struct {
	Body         io.ReadCloser
	ResourceKind ResourceKind
	Identifier   string
	Format       Format
	Filename     string
	ContentType  string
}

// Prepared is the fully validated, network-independent identity of an RCSB
// file request. Callers use it to resolve durable idempotency before touching
// the upstream service.
type Prepared struct {
	ResourceKind ResourceKind
	Identifier   string
	Format       Format
	Filename     string
}

type Fetcher interface {
	Fetch(context.Context, string, securefetch.Policy) (*securefetch.Response, error)
}

type Options struct {
	Fetcher                   Fetcher
	BaseURL                   string
	TestOnlyAllowCustomOrigin bool
	MaxBytes                  int64
	Timeout                   time.Duration
}

type Client struct {
	fetcher     Fetcher
	baseURL     string
	allowedHost string
	allowedPort string
	maxBytes    int64
	timeout     time.Duration
	configErr   error
}

var (
	classicEntryPattern = regexp.MustCompile(`^[0-9][A-Z0-9]{3}$`)
	componentPattern    = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)
	filenamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	ErrFileNotFound     = errors.New("rcsb_file_not_found")
)

func New(options Options) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = productionBaseURL
	}
	configErr := error(nil)
	origin, err := url.Parse(baseURL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" ||
		(baseURL != productionBaseURL && !options.TestOnlyAllowCustomOrigin) {
		configErr = errors.New("rcsb_file_invalid_configuration")
	}
	allowedHost := "files.rcsb.org"
	allowedPort := "443"
	if origin != nil && origin.Hostname() != "" {
		allowedHost = strings.ToLower(origin.Hostname())
		if origin.Port() != "" {
			allowedPort = origin.Port()
		}
	}
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = securefetch.New(securefetch.Options{})
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		configErr = errors.New("rcsb_file_invalid_configuration")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		configErr = errors.New("rcsb_file_invalid_configuration")
	}
	return &Client{
		fetcher: fetcher, baseURL: baseURL, allowedHost: allowedHost, allowedPort: allowedPort,
		maxBytes: maxBytes, timeout: timeout, configErr: configErr,
	}
}

func (c *Client) Fetch(ctx context.Context, input Input) (Result, error) {
	if c.configErr != nil {
		return Result{}, c.configErr
	}
	normalized, target, filename, mediaTypes, err := c.normalize(input)
	if err != nil {
		return Result{}, err
	}
	identity := buildinfo.Release()
	response, err := c.fetcher.Fetch(ctx, target, securefetch.Policy{
		AllowedHosts: []string{c.allowedHost}, AllowedPorts: []string{c.allowedPort}, AcceptedMediaTypes: mediaTypes,
		MaxRedirects: 3, MaxBytes: c.maxBytes, Timeout: c.timeout,
		UserAgent: identity.MachineSlug + "-rcsb-files/" + identity.Version,
	})
	if err != nil {
		if status, ok := securefetch.HTTPStatus(err); ok {
			switch {
			case status == 404:
				return Result{}, ErrFileNotFound
			case status == 429:
				return Result{}, errors.New("rcsb_file_rate_limited")
			case status >= 500:
				return Result{}, errors.New("rcsb_file_upstream_unavailable")
			default:
				return Result{}, errors.New("rcsb_file_download_failed")
			}
		}
		return Result{}, err
	}
	reader := bufio.NewReaderSize(response.Body, probeBytes)
	probe, probeErr := reader.Peek(probeBytes)
	if probeErr != nil && !errors.Is(probeErr, io.EOF) && !errors.Is(probeErr, bufio.ErrBufferFull) {
		response.Body.Close()
		return Result{}, errors.New("rcsb_file_read_failed")
	}
	if err := validatePrefix(probe); err != nil {
		response.Body.Close()
		return Result{}, err
	}
	return Result{
		Body: &validatingReadCloser{
			reader: reader, closer: response.Body, kind: normalized.ResourceKind,
			identifier: strings.ToLower(normalized.Identifier), format: normalized.Format,
		}, ResourceKind: normalized.ResourceKind,
		Identifier: normalized.Identifier, Format: normalized.Format, Filename: filename,
		ContentType: response.ContentType,
	}, nil
}

// Prepare validates a request and returns the exact identity and filename that
// Fetch will use without performing DNS, network, or filesystem I/O.
func Prepare(input Input) (Prepared, error) {
	normalized, filename, _, err := normalizeInput(input)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		ResourceKind: normalized.ResourceKind,
		Identifier:   normalized.Identifier,
		Format:       normalized.Format,
		Filename:     filename,
	}, nil
}

type normalizedInput struct {
	ResourceKind ResourceKind
	Identifier   string
	Format       Format
}

func (c *Client) normalize(input Input) (normalizedInput, string, string, []string, error) {
	normalized, filename, mediaTypes, err := normalizeInput(input)
	if err != nil {
		return normalizedInput{}, "", "", nil, err
	}
	path := "/download/" + normalized.Identifier + "." + string(normalized.Format)
	if normalized.ResourceKind == LigandDefinition {
		path = "/ligands/download/" + normalized.Identifier + ".cif"
	}
	return normalized, c.baseURL + path, filename, mediaTypes, nil
}

func normalizeInput(input Input) (normalizedInput, string, []string, error) {
	kind := input.ResourceKind
	if kind == "" {
		kind = EntryCoordinates
	}
	format := Format(strings.ToLower(strings.TrimSpace(string(input.Format))))
	entryID := strings.ToUpper(strings.TrimSpace(input.EntryID))
	componentID := strings.ToUpper(strings.TrimSpace(input.ComponentID))
	identifier := ""
	mediaTypes := []string{"chemical/x-cif", "text/plain", "application/octet-stream"}
	switch kind {
	case EntryCoordinates:
		if !classicEntryPattern.MatchString(entryID) || componentID != "" || (format != FormatPDB && format != FormatCIF) {
			return normalizedInput{}, "", nil, errors.New("rcsb_file_invalid_request")
		}
		identifier = entryID
		if format == FormatPDB {
			mediaTypes = []string{"chemical/x-pdb", "text/plain", "application/octet-stream"}
		}
	case LigandDefinition:
		if entryID != "" || !componentPattern.MatchString(componentID) || format != FormatCIF {
			return normalizedInput{}, "", nil, errors.New("rcsb_file_invalid_request")
		}
		identifier = componentID
	default:
		return normalizedInput{}, "", nil, errors.New("rcsb_file_invalid_request")
	}
	filename := strings.TrimSpace(input.Filename)
	if filename == "" {
		filename = identifier + "." + string(format)
	}
	if !filenamePattern.MatchString(filename) || filepath.Base(filename) != filename || !strings.EqualFold(filepath.Ext(filename), "."+string(format)) {
		return normalizedInput{}, "", nil, errors.New("rcsb_file_invalid_request")
	}
	return normalizedInput{ResourceKind: kind, Identifier: identifier, Format: format}, filename, mediaTypes, nil
}

func validatePrefix(content []byte) error {
	if len(content) == 0 || strings.IndexByte(string(content), 0) >= 0 {
		return errors.New("rcsb_file_content_invalid")
	}
	text := strings.TrimSpace(string(content))
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return errors.New("rcsb_file_content_invalid")
	}
	return nil
}

type validatingReadCloser struct {
	reader       io.Reader
	closer       io.Closer
	kind         ResourceKind
	identifier   string
	format       Format
	tail         string
	seenData     bool
	seenAtomSite bool
	seenChemComp bool
	seenChemID   bool
	seenChemAtom bool
	seenPDBID    bool
	seenPDBAtom  bool
	invalid      bool
	finalized    bool
	cifMultiline bool
	currentData  string
	wantChemID   bool
	inLoop       bool
	loopHasRows  bool
	loopFields   []string
	loopRow      []cifToken
	loopGroup    int
	loopX        int
	loopY        int
	loopZ        int
	loopComp     int
	loopAtom     int
}

func (r *validatingReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.observe(buffer[:n])
	}
	if r.invalid {
		return n, errors.New("rcsb_file_content_invalid")
	}
	if errors.Is(err, io.EOF) {
		r.finalize()
		if r.invalid || !r.valid() {
			return n, errors.New("rcsb_file_content_invalid")
		}
	}
	return n, err
}

func (r *validatingReadCloser) Close() error { return r.closer.Close() }

func (r *validatingReadCloser) observe(content []byte) {
	if strings.IndexByte(string(content), 0) >= 0 {
		r.invalid = true
		return
	}
	lines := strings.Split(r.tail+string(content), "\n")
	for _, line := range lines[:len(lines)-1] {
		r.observeLine(strings.TrimSuffix(line, "\r"))
	}
	r.tail = lines[len(lines)-1]
	if len(r.tail) > 64<<10 {
		r.invalid = true
	}
}

func (r *validatingReadCloser) finalize() {
	if r.finalized {
		return
	}
	r.finalized = true
	if r.tail != "" {
		r.observeLine(strings.TrimSuffix(r.tail, "\r"))
	}
	if r.cifMultiline || r.wantChemID {
		r.invalid = true
	}
	r.finishCIFLoop()
	r.tail = ""
}

func (r *validatingReadCloser) observeLine(line string) {
	if r.format == FormatPDB {
		if len(line) >= 66 && line[:6] == "HEADER" && strings.EqualFold(strings.TrimSpace(line[62:66]), r.identifier) {
			r.seenPDBID = true
		}
		if validPDBCoordinateLine(line) {
			r.seenPDBAtom = true
		}
		return
	}
	if r.cifMultiline {
		if strings.HasPrefix(line, ";") {
			r.cifMultiline = false
			r.observeCIFToken(cifToken{value: "?", quoted: true})
		}
		return
	}
	if strings.HasPrefix(line, ";") {
		r.cifMultiline = true
		return
	}
	tokens, valid := tokenizeCIFLine(line)
	if !valid {
		r.invalid = true
		return
	}
	for _, token := range tokens {
		r.observeCIFToken(token)
	}
}

func (r *validatingReadCloser) observeCIFToken(token cifToken) {
	value := strings.ToLower(token.value)
	if r.wantChemID {
		if !token.quoted && isCIFControlToken(value) {
			r.invalid = true
		} else {
			if r.seenChemID {
				r.invalid = true
			}
			r.seenChemID = true
			r.seenChemComp = value == r.identifier
		}
		r.wantChemID = false
		if r.invalid {
			return
		}
	}
	if !token.quoted && strings.HasPrefix(value, "data_") {
		r.finishCIFLoop()
		r.currentData = strings.TrimPrefix(value, "data_")
		if r.identifier == "" && r.kind == "" {
			r.identifier = r.currentData
		}
		if r.currentData == r.identifier {
			if r.seenData {
				r.invalid = true
			}
			r.seenData = true
		}
		return
	}
	if !token.quoted && value == "loop_" {
		r.finishCIFLoop()
		r.inLoop = true
		return
	}
	if !token.quoted && value == "stop_" {
		r.finishCIFLoop()
		return
	}
	if r.inLoop {
		if !r.loopHasRows && len(r.loopRow) == 0 && !token.quoted && strings.HasPrefix(value, "_") {
			r.loopFields = append(r.loopFields, value)
			return
		}
		if len(r.loopFields) == 0 {
			r.invalid = true
			return
		}
		if len(r.loopRow) == 0 && !token.quoted && strings.HasPrefix(value, "_") {
			r.finishCIFLoop()
			r.observeCIFToken(token)
			return
		}
		r.loopRow = append(r.loopRow, token)
		if len(r.loopRow) == len(r.loopFields) {
			if !r.loopHasRows {
				r.prepareCIFLoop()
			}
			r.observeCIFLoopRow()
			r.loopRow = r.loopRow[:0]
			r.loopHasRows = true
		} else if len(r.loopRow) > len(r.loopFields) {
			r.invalid = true
		}
		return
	}
	if r.currentData != r.identifier || token.quoted {
		return
	}
	if value == "_chem_comp.id" {
		r.wantChemID = true
	}
}

func isCIFControlToken(value string) bool {
	return value == "loop_" || value == "stop_" || strings.HasPrefix(value, "data_") || strings.HasPrefix(value, "_")
}

func (r *validatingReadCloser) finishCIFLoop() {
	if r.inLoop && (len(r.loopFields) == 0 || !r.loopHasRows || len(r.loopRow) != 0) {
		r.invalid = true
	}
	r.inLoop = false
	r.loopHasRows = false
	r.loopFields = nil
	r.loopRow = nil
	r.loopGroup, r.loopX, r.loopY, r.loopZ = -1, -1, -1, -1
	r.loopComp, r.loopAtom = -1, -1
}

func (r *validatingReadCloser) prepareCIFLoop() {
	r.loopGroup, r.loopX, r.loopY, r.loopZ = -1, -1, -1, -1
	r.loopComp, r.loopAtom = -1, -1
	for index, field := range r.loopFields {
		switch field {
		case "_atom_site.group_pdb":
			r.loopGroup = index
		case "_atom_site.cartn_x":
			r.loopX = index
		case "_atom_site.cartn_y":
			r.loopY = index
		case "_atom_site.cartn_z":
			r.loopZ = index
		case "_chem_comp_atom.comp_id":
			r.loopComp = index
		case "_chem_comp_atom.atom_id":
			r.loopAtom = index
		}
	}
}

func (r *validatingReadCloser) observeCIFLoopRow() {
	if r.currentData != r.identifier {
		return
	}
	if r.loopGroup >= 0 && r.loopX >= 0 && r.loopY >= 0 && r.loopZ >= 0 {
		if isAtomGroup(r.loopRow[r.loopGroup].value) &&
			validCIFFloat(r.loopRow[r.loopX].value) && validCIFFloat(r.loopRow[r.loopY].value) && validCIFFloat(r.loopRow[r.loopZ].value) {
			r.seenAtomSite = true
		}
	}
	if r.loopComp >= 0 && r.loopAtom >= 0 {
		if strings.EqualFold(r.loopRow[r.loopComp].value, r.identifier) && validCIFValue(r.loopRow[r.loopAtom].value) {
			r.seenChemAtom = true
		}
	}
}

func isAtomGroup(value string) bool {
	return strings.EqualFold(value, "ATOM") || strings.EqualFold(value, "HETATM")
}

func validCIFFloat(value string) bool {
	if !validCIFValue(value) {
		return false
	}
	number, err := strconv.ParseFloat(value, 64)
	return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func validCIFValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != "?"
}

func validPDBCoordinateLine(line string) bool {
	if len(line) < 54 || (line[:6] != "ATOM  " && line[:6] != "HETATM") {
		return false
	}
	serial, err := strconv.Atoi(strings.TrimSpace(line[6:11]))
	if err != nil || serial <= 0 {
		return false
	}
	for _, field := range []string{line[30:38], line[38:46], line[46:54]} {
		value, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

type cifToken struct {
	value  string
	quoted bool
}

func tokenizeCIFLine(line string) ([]cifToken, bool) {
	tokens := make([]cifToken, 0, 4)
	for index := 0; index < len(line); {
		for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
			index++
		}
		if index == len(line) || line[index] == '#' {
			break
		}
		if line[index] == '\'' || line[index] == '"' {
			quote := line[index]
			index++
			start := index
			for index < len(line) && line[index] != quote {
				index++
			}
			if index == len(line) {
				return tokens, false
			}
			tokens = append(tokens, cifToken{value: line[start:index], quoted: true})
			index++
			continue
		}
		start := index
		for index < len(line) && line[index] != ' ' && line[index] != '\t' && line[index] != '#' {
			index++
		}
		if start < index {
			tokens = append(tokens, cifToken{value: line[start:index]})
		}
		if index < len(line) && line[index] == '#' {
			break
		}
	}
	return tokens, true
}

func (r *validatingReadCloser) valid() bool {
	if r.format == FormatPDB {
		return r.seenPDBID && r.seenPDBAtom
	}
	if r.kind == EntryCoordinates {
		return r.seenData && r.seenAtomSite
	}
	if r.kind == "" {
		return r.seenData && (r.seenAtomSite || r.seenChemComp && r.seenChemAtom)
	}
	return r.kind == LigandDefinition && r.seenData && r.seenChemComp && r.seenChemAtom
}
