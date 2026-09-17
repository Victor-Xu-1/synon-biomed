package bindingmode

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://files.rcsb.org/download"
const defaultMaxBytes = int64(32 << 20)

var waterNames = map[string]struct{}{"HOH": {}, "WAT": {}, "DOD": {}}
var commonIons = map[string]struct{}{"NA": {}, "K": {}, "CL": {}, "CA": {}, "MG": {}, "MN": {}, "ZN": {}, "FE": {}, "CU": {}, "NI": {}, "CO": {}, "CD": {}, "HG": {}}
var metals = map[string]struct{}{"ZN": {}, "FE": {}, "MG": {}, "MN": {}, "CA": {}, "CU": {}, "NI": {}, "CO": {}, "CD": {}, "HG": {}, "NA": {}, "K": {}}
var polarElements = map[string]struct{}{"N": {}, "O": {}, "S": {}, "P": {}}

type Input struct {
	PDBID          string
	LigandResidue  string
	Chain          string
	MaxContacts    int
	CutoffAngstrom float64
}

type Options struct {
	HTTPClient *http.Client
	BaseURL    string
	MaxBytes   int64
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	maxBytes   int64
}

type atom struct {
	record, atomName, resName, chain, resSeq, iCode, element string
	serial                                                   int
	x, y, z, occupancy                                       float64
}

type ligandCandidate struct {
	resName, chain, resSeq, iCode string
	atoms                         []atom
}

type contact struct {
	typ            string
	distance       float64
	ligandAtom     string
	proteinAtom    string
	proteinResidue string
	ligandElement  string
	proteinElement string
}

func NewClient(options Options) *Client {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	return &Client{httpClient: httpClient, baseURL: baseURL, maxBytes: maxBytes}
}

func (c *Client) Analyze(ctx context.Context, input Input) (map[string]any, error) {
	if c == nil {
		c = NewClient(Options{})
	}
	pdbID := strings.ToUpper(strings.TrimSpace(input.PDBID))
	if !validPDBID(pdbID) {
		return nil, fmt.Errorf("binding_mode_analysis requires a valid four-character PDB ID, got %q", input.PDBID)
	}
	maxContacts := input.MaxContacts
	if maxContacts <= 0 {
		maxContacts = 40
	}
	if maxContacts > 200 {
		maxContacts = 200
	}
	cutoff := input.CutoffAngstrom
	if cutoff == 0 {
		cutoff = 4
	}
	cutoff = math.Max(2, math.Min(8, cutoff))

	started := time.Now()
	pdbURL := c.baseURL + "/" + pdbID + ".pdb"
	coordinateURL := pdbURL
	coordinateFormat := "pdb"
	coordinateText, status, err := c.fetchCoordinates(ctx, pdbURL, "chemical/x-pdb,text/plain,*/*")
	if err != nil && status == http.StatusNotFound {
		coordinateURL = c.baseURL + "/" + pdbID + ".cif"
		coordinateFormat = "cif"
		coordinateText, _, err = c.fetchCoordinates(ctx, coordinateURL, "chemical/x-cif,text/plain,*/*")
	}
	if err != nil {
		return nil, err
	}
	atoms := parsePDBAtoms(coordinateText)
	parser := "pdb_distance_contacts_v1"
	if coordinateFormat == "cif" {
		atoms = parseCIFAtoms(coordinateText)
		parser = "mmcif_distance_contacts_v1"
	}
	candidates := ligandCandidates(atoms)
	ligand := selectLigand(candidates, input)
	if ligand == nil {
		return noLigandOutput(pdbID, coordinateURL, len(coordinateText), len(atoms), started), nil
	}

	proteinAtoms := make([]atom, 0, len(atoms))
	for _, candidate := range atoms {
		if candidate.record == "ATOM" && candidate.element != "H" {
			proteinAtoms = append(proteinAtoms, candidate)
		}
	}
	contacts := calculateContacts(ligand.atoms, proteinAtoms, cutoff)
	selected := contacts
	if len(selected) > maxContacts {
		selected = selected[:maxContacts]
	}
	byType, residues := summarizeContacts(contacts)
	ligandRows := make([]map[string]any, 0, minInt(10, len(candidates)))
	for _, candidate := range candidates {
		if len(ligandRows) == 10 {
			break
		}
		carbonCount := 0
		for _, candidateAtom := range candidate.atoms {
			if candidateAtom.element == "C" {
				carbonCount++
			}
		}
		ligandRows = append(ligandRows, map[string]any{
			"resName": candidate.resName, "chain": candidate.chain, "resSeq": candidate.resSeq,
			"atomCount": len(candidate.atoms), "carbonCount": carbonCount,
		})
	}
	contactRows := make([]map[string]any, 0, len(selected))
	for _, item := range selected {
		contactRows = append(contactRows, map[string]any{
			"type": item.typ, "distanceAngstrom": item.distance,
			"ligandAtom": item.ligandAtom, "proteinAtom": item.proteinAtom,
			"proteinResidue": item.proteinResidue, "ligandElement": item.ligandElement,
			"proteinElement": item.proteinElement,
		})
	}
	sourceURL := "https://www.rcsb.org/structure/" + pdbID
	output := map[string]any{
		"pdbId": pdbID,
		"structure": map[string]any{
			"url": sourceURL, "coordinateDownloadUrl": coordinateURL, "coordinateFormat": coordinateFormat,
			"atomCount": len(atoms), "proteinAtomCount": len(proteinAtoms),
		},
		"ligand": map[string]any{
			"resName": ligand.resName, "chain": ligand.chain, "resSeq": ligand.resSeq, "atomCount": len(ligand.atoms),
		},
		"ligandCandidates": ligandRows,
		"contacts":         contactRows,
		"interactionSummary": map[string]any{
			"totalContacts": len(contacts), "byType": byType, "topResidues": residues,
		},
		"interactionDiagram": buildInteractionDiagram(pdbID, *ligand, contacts),
		"sources": []map[string]any{{
			"title": "RCSB PDB " + pdbID, "url": sourceURL, "provider": "binding_mode_analysis", "rank": 1,
			"snippet":  fmt.Sprintf("Distance-based ligand binding mode analysis for %s %s%s.", ligand.resName, ligand.chain, ligand.resSeq),
			"metadata": map[string]any{"pdbId": pdbID, "ligand": ligand.resName, "cutoffAngstrom": cutoff},
		}},
		"durationSeconds": time.Since(started).Seconds(),
		"diagnostics": map[string]any{
			"downloadedBytes": len(coordinateText), "parser": parser, "cutoffAngstrom": cutoff,
			"returnedContacts": len(selected), "diagram": "residue_interaction_svg_v1",
		},
	}
	if len(contacts) == 0 {
		output["failure"] = map[string]any{"kind": "binding_contacts_empty", "message": "No ligand-protein contacts were found within the configured cutoff."}
	}
	return output, nil
}

func (c *Client) fetchCoordinates(ctx context.Context, target, accept string) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("Accept", accept)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("download RCSB structure: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", response.StatusCode, fmt.Errorf("RCSB coordinate download returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		return "", response.StatusCode, fmt.Errorf("read RCSB structure: %w", err)
	}
	if int64(len(raw)) > c.maxBytes {
		return "", response.StatusCode, fmt.Errorf("RCSB structure exceeds %d bytes", c.maxBytes)
	}
	return string(raw), response.StatusCode, nil
}

func validPDBID(value string) bool {
	if len(value) != 4 || value[0] < '0' || value[0] > '9' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func parsePDBAtoms(text string) []atom {
	atoms := make([]atom, 0)
	for _, line := range strings.Split(text, "\n") {
		if len(line) < 54 {
			continue
		}
		record := strings.TrimSpace(slice(line, 0, 6))
		if record != "ATOM" && record != "HETATM" {
			continue
		}
		x, xErr := strconv.ParseFloat(strings.TrimSpace(slice(line, 30, 38)), 64)
		y, yErr := strconv.ParseFloat(strings.TrimSpace(slice(line, 38, 46)), 64)
		z, zErr := strconv.ParseFloat(strings.TrimSpace(slice(line, 46, 54)), 64)
		if xErr != nil || yErr != nil || zErr != nil {
			continue
		}
		serial, _ := strconv.Atoi(strings.TrimSpace(slice(line, 6, 11)))
		occupancy, _ := strconv.ParseFloat(strings.TrimSpace(slice(line, 54, 60)), 64)
		atomName := strings.TrimSpace(slice(line, 12, 16))
		atoms = append(atoms, atom{
			record: record, serial: serial, atomName: atomName,
			resName: strings.TrimSpace(slice(line, 17, 20)), chain: strings.TrimSpace(slice(line, 21, 22)),
			resSeq: strings.TrimSpace(slice(line, 22, 26)), iCode: strings.TrimSpace(slice(line, 26, 27)),
			x: x, y: y, z: z, occupancy: occupancy,
			element: normalizeElement(slice(line, 76, 80), atomName),
		})
	}
	return atoms
}

func parseCIFAtoms(text string) []atom {
	lines := strings.Split(text, "\n")
	atoms := make([]atom, 0)
	for index := 0; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "loop_" {
			continue
		}
		fields := make([]string, 0)
		for index+1 < len(lines) {
			line := strings.TrimSpace(lines[index+1])
			if !strings.HasPrefix(strings.ToLower(line), "_atom_site.") {
				break
			}
			fields = append(fields, strings.ToLower(strings.Fields(line)[0]))
			index++
		}
		if len(fields) == 0 {
			continue
		}
		positions := make(map[string]int, len(fields))
		for fieldIndex, field := range fields {
			positions[field] = fieldIndex
		}
		rowTokens := make([]string, 0, len(fields))
		for index+1 < len(lines) {
			line := strings.TrimSpace(lines[index+1])
			if line == "" || strings.HasPrefix(line, "#") {
				index++
				if len(rowTokens) == 0 {
					break
				}
				continue
			}
			if line == "loop_" || strings.HasPrefix(line, "_") || strings.HasPrefix(strings.ToLower(line), "data_") {
				break
			}
			rowTokens = append(rowTokens, splitCIFRow(line)...)
			index++
			for len(rowTokens) >= len(fields) {
				row := rowTokens[:len(fields)]
				rowTokens = rowTokens[len(fields):]
				if parsed, ok := cifAtomFromRow(row, positions); ok {
					atoms = append(atoms, parsed)
				}
			}
		}
	}
	return atoms
}

func splitCIFRow(line string) []string {
	values := make([]string, 0)
	for index := 0; index < len(line); {
		for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
			index++
		}
		if index >= len(line) || line[index] == '#' {
			break
		}
		if line[index] == '\'' || line[index] == '"' {
			quote := line[index]
			index++
			start := index
			for index < len(line) && line[index] != quote {
				index++
			}
			values = append(values, line[start:index])
			if index < len(line) {
				index++
			}
			continue
		}
		start := index
		for index < len(line) && line[index] != ' ' && line[index] != '\t' {
			index++
		}
		values = append(values, line[start:index])
	}
	return values
}

func cifAtomFromRow(row []string, positions map[string]int) (atom, bool) {
	get := func(names ...string) string {
		for _, name := range names {
			if index, ok := positions[name]; ok && index < len(row) {
				value := row[index]
				if value != "." && value != "?" {
					return value
				}
			}
		}
		return ""
	}
	record := strings.ToUpper(get("_atom_site.group_pdb"))
	if record != "ATOM" && record != "HETATM" {
		return atom{}, false
	}
	x, xErr := strconv.ParseFloat(get("_atom_site.cartn_x"), 64)
	y, yErr := strconv.ParseFloat(get("_atom_site.cartn_y"), 64)
	z, zErr := strconv.ParseFloat(get("_atom_site.cartn_z"), 64)
	if xErr != nil || yErr != nil || zErr != nil || math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(z) ||
		math.IsInf(x, 0) || math.IsInf(y, 0) || math.IsInf(z, 0) {
		return atom{}, false
	}
	serial, _ := strconv.Atoi(get("_atom_site.id"))
	occupancy, _ := strconv.ParseFloat(get("_atom_site.occupancy"), 64)
	atomName := get("_atom_site.auth_atom_id", "_atom_site.label_atom_id")
	return atom{
		record: record, serial: serial, atomName: atomName,
		resName: get("_atom_site.auth_comp_id", "_atom_site.label_comp_id"),
		chain:   get("_atom_site.auth_asym_id", "_atom_site.label_asym_id"),
		resSeq:  get("_atom_site.auth_seq_id", "_atom_site.label_seq_id"),
		iCode:   get("_atom_site.pdbx_pdb_ins_code"),
		x:       x, y: y, z: z, occupancy: occupancy,
		element: normalizeElement(get("_atom_site.type_symbol"), atomName),
	}, true
}

func slice(value string, start, end int) string {
	if start >= len(value) {
		return ""
	}
	if end > len(value) {
		end = len(value)
	}
	return value[start:end]
}

func normalizeElement(rawElement, atomName string) string {
	element := lettersUpper(rawElement)
	if len(element) >= 2 {
		if _, ok := metals[element[:2]]; ok {
			return element[:2]
		}
	}
	if element != "" {
		return element[:1]
	}
	element = lettersUpper(atomName)
	if len(element) >= 2 {
		if _, ok := metals[element[:2]]; ok {
			return element[:2]
		}
	}
	if element != "" {
		return element[:1]
	}
	return ""
}

func lettersUpper(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToUpper(value) {
		if char >= 'A' && char <= 'Z' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func ligandCandidates(atoms []atom) []ligandCandidate {
	groups := map[string]*ligandCandidate{}
	for _, candidateAtom := range atoms {
		if candidateAtom.record != "HETATM" || candidateAtom.element == "H" {
			continue
		}
		if _, ok := waterNames[candidateAtom.resName]; ok {
			continue
		}
		if _, ok := commonIons[candidateAtom.resName]; ok {
			continue
		}
		key := strings.Join([]string{candidateAtom.resName, candidateAtom.chain, candidateAtom.resSeq, candidateAtom.iCode}, ":")
		group := groups[key]
		if group == nil {
			group = &ligandCandidate{resName: candidateAtom.resName, chain: candidateAtom.chain, resSeq: candidateAtom.resSeq, iCode: candidateAtom.iCode}
			groups[key] = group
		}
		group.atoms = append(group.atoms, candidateAtom)
	}
	result := make([]ligandCandidate, 0, len(groups))
	for _, group := range groups {
		carbons := 0
		for _, candidateAtom := range group.atoms {
			if candidateAtom.element == "C" {
				carbons++
			}
		}
		if len(group.atoms) >= 5 && carbons >= 3 {
			result = append(result, *group)
		}
	}
	sort.Slice(result, func(i, j int) bool { return len(result[i].atoms) > len(result[j].atoms) })
	return result
}

func selectLigand(candidates []ligandCandidate, input Input) *ligandCandidate {
	requested := strings.ToUpper(strings.TrimSpace(input.LigandResidue))
	chain := strings.ToUpper(strings.TrimSpace(input.Chain))
	if requested != "" {
		for index := range candidates {
			if candidates[index].resName == requested && (chain == "" || strings.EqualFold(candidates[index].chain, chain)) {
				return &candidates[index]
			}
		}
		return nil
	}
	if chain != "" {
		for index := range candidates {
			if strings.EqualFold(candidates[index].chain, chain) {
				return &candidates[index]
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0]
}

func calculateContacts(ligandAtoms, proteinAtoms []atom, cutoff float64) []contact {
	cutoffSquared := cutoff * cutoff
	contacts := make([]contact, 0)
	for _, ligandAtom := range ligandAtoms {
		for _, proteinAtom := range proteinAtoms {
			dx, dy, dz := ligandAtom.x-proteinAtom.x, ligandAtom.y-proteinAtom.y, ligandAtom.z-proteinAtom.z
			distanceSquared := dx*dx + dy*dy + dz*dz
			if distanceSquared > cutoffSquared {
				continue
			}
			distance := math.Sqrt(distanceSquared)
			contacts = append(contacts, contact{
				typ: classifyContact(ligandAtom, proteinAtom, distance), distance: math.Round(distance*100) / 100,
				ligandAtom: atomLabel(ligandAtom), proteinAtom: atomLabel(proteinAtom),
				proteinResidue: fmt.Sprintf("%s %s%s", proteinAtom.resName, proteinAtom.chain, proteinAtom.resSeq),
				ligandElement:  ligandAtom.element, proteinElement: proteinAtom.element,
			})
		}
	}
	sort.SliceStable(contacts, func(i, j int) bool { return contacts[i].distance < contacts[j].distance })
	return contacts
}

func classifyContact(ligandAtom, proteinAtom atom, distance float64) string {
	if _, ok := metals[ligandAtom.element]; ok {
		return "metal_coordination"
	}
	if _, ok := metals[proteinAtom.element]; ok {
		return "metal_coordination"
	}
	if distance <= 2.2 {
		return "close_contact"
	}
	_, ligandPolar := polarElements[ligandAtom.element]
	_, proteinPolar := polarElements[proteinAtom.element]
	if ligandPolar && proteinPolar && distance <= 3.5 {
		return "polar_contact"
	}
	if ligandAtom.element == "C" && proteinAtom.element == "C" && distance <= 4 {
		return "hydrophobic_contact"
	}
	return "van_der_waals_contact"
}

func atomLabel(value atom) string {
	return fmt.Sprintf("%s %s%s:%s", value.resName, value.chain, value.resSeq, value.atomName)
}

func summarizeContacts(contacts []contact) (map[string]int, []map[string]any) {
	byType := map[string]int{}
	residueCounts := map[string]int{}
	for _, item := range contacts {
		byType[item.typ]++
		residueCounts[item.proteinResidue]++
	}
	type pair struct {
		residue string
		count   int
	}
	pairs := make([]pair, 0, len(residueCounts))
	for residue, count := range residueCounts {
		pairs = append(pairs, pair{residue: residue, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count == pairs[j].count {
			return pairs[i].residue < pairs[j].residue
		}
		return pairs[i].count > pairs[j].count
	})
	if len(pairs) > 12 {
		pairs = pairs[:12]
	}
	rows := make([]map[string]any, 0, len(pairs))
	for _, item := range pairs {
		rows = append(rows, map[string]any{"residue": item.residue, "count": item.count})
	}
	return byType, rows
}

func buildInteractionDiagram(pdbID string, ligand ligandCandidate, contacts []contact) map[string]any {
	type residueContact struct {
		name, typ string
		distance  float64
	}
	byResidue := map[string]residueContact{}
	for _, item := range contacts {
		current, ok := byResidue[item.proteinResidue]
		if !ok || item.distance < current.distance {
			byResidue[item.proteinResidue] = residueContact{name: item.proteinResidue, typ: item.typ, distance: item.distance}
		}
	}
	residues := make([]residueContact, 0, len(byResidue))
	for _, item := range byResidue {
		residues = append(residues, item)
	}
	sort.Slice(residues, func(i, j int) bool { return residues[i].distance < residues[j].distance })
	if len(residues) > 12 {
		residues = residues[:12]
	}
	const width, height = 960, 720
	cx, cy := float64(width)/2, float64(height)/2+10
	radius := 255.0
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="960" height="720" viewBox="0 0 960 720" role="img">`)
	svg.WriteString(`<rect width="960" height="720" fill="#ffffff"/><text x="40" y="48" font-family="Arial,sans-serif" font-size="24" font-weight="700" fill="#111827">`)
	svg.WriteString(xmlEscape("Protein-ligand interaction map: " + pdbID))
	svg.WriteString(`</text><text x="40" y="76" font-family="Arial,sans-serif" font-size="14" fill="#6b7280">Distance-based structural triage; verify geometry before publication.</text>`)
	for index, item := range residues {
		angle := (2*math.Pi*float64(index))/math.Max(1, float64(len(residues))) - math.Pi/2
		x, y := cx+radius*math.Cos(angle), cy+radius*math.Sin(angle)
		color, dash := contactStyle(item.typ)
		svg.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="3"%s/>`, cx, cy, x, y, color, dash))
		svg.WriteString(fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="150" height="54" rx="6" fill="#ffffff" stroke="#d1d5db"/>`, x-75, y-27))
		svg.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-family="Arial,sans-serif" font-size="15" font-weight="700" fill="#111827">%s</text>`, x, y-3, xmlEscape(item.name)))
		svg.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-family="Arial,sans-serif" font-size="12" fill="#6b7280">%s · %.2f Å</text>`, x, y+17, xmlEscape(strings.ReplaceAll(item.typ, "_", " ")), item.distance))
	}
	svg.WriteString(fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="190" height="86" rx="8" fill="#111827"/>`, cx-95, cy-43))
	svg.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-family="Arial,sans-serif" font-size="22" font-weight="700" fill="#ffffff">%s</text>`, cx, cy-5, xmlEscape(ligand.resName)))
	svg.WriteString(fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="middle" font-family="Arial,sans-serif" font-size="14" fill="#d1d5db">chain %s · residue %s</text>`, cx, cy+23, xmlEscape(ligand.chain), xmlEscape(ligand.resSeq)))
	svg.WriteString(`</svg>`)
	return map[string]any{
		"format": "svg", "width": width, "height": height, "svg": svg.String(),
		"kind": "residue_interaction_map", "residueCount": len(residues),
	}
}

func contactStyle(kind string) (string, string) {
	switch kind {
	case "polar_contact":
		return "#2563eb", ` stroke-dasharray="7 5"`
	case "hydrophobic_contact":
		return "#16a34a", ""
	case "metal_coordination":
		return "#7c3aed", ` stroke-dasharray="3 4"`
	case "close_contact":
		return "#dc2626", ""
	default:
		return "#6b7280", ` stroke-dasharray="2 5"`
	}
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}

func noLigandOutput(pdbID, pdbURL string, downloadedBytes, atomCount int, started time.Time) map[string]any {
	return map[string]any{
		"pdbId":            pdbID,
		"structure":        map[string]any{"url": "https://www.rcsb.org/structure/" + pdbID, "pdbDownloadUrl": pdbURL, "atomCount": atomCount},
		"ligandCandidates": []map[string]any{}, "contacts": []map[string]any{},
		"interactionSummary": map[string]any{"totalContacts": 0, "byType": map[string]int{}, "topResidues": []map[string]any{}},
		"durationSeconds":    time.Since(started).Seconds(),
		"diagnostics":        map[string]any{"downloadedBytes": downloadedBytes, "parser": "pdb_distance_contacts_v1"},
		"failure":            map[string]any{"kind": "binding_ligand_not_found", "message": "No non-water organic HETATM ligand candidate was found in the PDB file."},
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
