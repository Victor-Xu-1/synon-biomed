package server

import (
	"net/url"
	"strings"
)

// isLikelyScientificDownloadURL identifies file-shaped public URLs whose
// identity must remain model-selected until the dedicated download contract
// validates them. It deliberately excludes ordinary HTML/text pages so the
// research continuation policy can still bind page fetches to its durable
// frontier.
func isLikelyScientificDownloadURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	path := strings.ToLower(parsed.Path)
	for _, suffix := range []string{
		".pdb", ".pdb.gz", ".cif", ".cif.gz", ".mmcif", ".mmcif.gz",
		".sdf", ".sdf.gz", ".mol", ".mol2", ".pqr", ".pdbqt",
		".tar", ".tar.gz", ".tgz", ".zip", ".7z", ".gz", ".bz2", ".xz",
		".pdf", ".csv", ".tsv", ".jsonl", ".fasta", ".fa", ".fastq", ".fq",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
