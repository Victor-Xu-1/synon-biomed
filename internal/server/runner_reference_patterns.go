package server

import "regexp"

var (
	sessionRunnerDOIPattern            = regexp.MustCompile(`(?i)10\.[0-9]{4,9}/[-._;()/:a-z0-9]+`)
	sessionRunnerDOIExactPattern       = regexp.MustCompile(`(?i)^10\.[0-9]{4,9}/[-._;()/:a-z0-9]+$`)
	sessionRunnerNCTPattern            = regexp.MustCompile(`(?i)\bNCT[0-9]{8}\b`)
	sessionRunnerNCTExactPattern       = regexp.MustCompile(`(?i)^NCT[0-9]{8}$`)
	sessionRunnerURLPattern            = regexp.MustCompile(`(?i)https?://[^\s<>"'，。；：！？、（）【】《》〈〉「」『』“”‘’]+`)
	sessionRunnerPMIDLabelPattern      = regexp.MustCompile(`(?i)\bPMIDs?\s*[:#=]?\s*([0-9][0-9\s,;/]{0,80})`)
	sessionRunnerPMIDTokenPattern      = regexp.MustCompile(`\b[1-9][0-9]{0,8}\b`)
	sessionRunnerPMIDExactPattern      = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	sessionRunnerPubMedXMLPattern      = regexp.MustCompile(`(?i)<PMID(?:\s[^>]*)?>\s*([1-9][0-9]{0,8})\s*</PMID>`)
	sessionRunnerPubMedUIDPattern      = regexp.MustCompile(`(?i)"uid"\s*:\s*"([1-9][0-9]{0,8})"`)
	sessionRunnerPubMedUIDsPattern     = regexp.MustCompile(`(?i)"uids"\s*:\s*\[([^]]*)\]`)
	sessionRunnerQuotedPMIDPattern     = regexp.MustCompile(`"([1-9][0-9]{0,8})"`)
	sessionRunnerEuropePMCQueryPattern = regexp.MustCompile(`(?i)^\s*\(?\s*EXT_ID\s*:\s*[1-9][0-9]{0,8}\s*\)?(?:\s+OR\s+\(?\s*EXT_ID\s*:\s*[1-9][0-9]{0,8}\s*\)?)*\s*$`)
	sessionRunnerEuropePMCIDPattern    = regexp.MustCompile(`(?i)\bEXT_ID\s*:\s*([1-9][0-9]{0,8})\b`)
	sessionRunnerPDBPattern            = regexp.MustCompile(`(?i)\bPDB\s*(?:ID\s*)?[:#=-]?\s*([0-9][A-Z0-9]{3})\b`)
	sessionRunnerPDBListPattern        = regexp.MustCompile(`(?i)\bPDB\s*(?:IDs?|entries)?\s*[:#=()\-]*\s*((?:[0-9][A-Z0-9]{3})(?:\s*[,;/]\s*[0-9][A-Z0-9]{3})+)`)
	sessionRunnerPDBLabelPattern       = regexp.MustCompile(`(?i)\bPDB\b(?:\s+(?:structural\s+)?(?:IDs?|entries|structures))?`)
	sessionRunnerPDBTokenPattern       = regexp.MustCompile(`(?i)\b[0-9][A-Z0-9]{3}\b`)
	sessionRunnerPDBExactPattern       = regexp.MustCompile(`(?i)^[0-9][A-Z0-9]{3}$`)
	sessionRunnerRCSBEntryPattern      = regexp.MustCompile(`(?i)"entry"\s*:\s*\{\s*"id"\s*:\s*"([0-9][A-Z0-9]{3})"`)
	sessionRunnerUniProtPattern        = regexp.MustCompile(`(?i)\bUniProt(?:KB)?(?:\s+(?:ID|accession))?\s*[:#=-]?\s*((?:(?:[OPQ][0-9][A-Z0-9]{3}[0-9])|(?:[A-NR-Z][0-9](?:[A-Z][A-Z0-9]{2}[0-9]){1,2}))(?:-[0-9]+)?)\b`)
	sessionRunnerUniProtExactPattern   = regexp.MustCompile(`(?i)^(?:(?:[OPQ][0-9][A-Z0-9]{3}[0-9])|(?:[A-NR-Z][0-9](?:[A-Z][A-Z0-9]{2}[0-9]){1,2}))(?:-[0-9]+)?$`)
	sessionRunnerNegativeDOIAfter      = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?DOI\s*[:#=]?\s*(10\.[0-9]{4,9}/[-._;()/:a-z0-9]+)\s+(?:[-—–:]\s*)?(?:NOT\s+FOUND|UNVERIFIED)\s+IN\s+CROSSREF\s*(?:\(\s*HTTP\s+404\s+VERIFIED\s*\))?\s*[.;]?\s*$`)
	sessionRunnerNegativeDOIBefore     = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?(?:NOT\s+FOUND|UNVERIFIED)\s+IN\s+CROSSREF\s*(?:[-—–:]\s*)?DOI\s*[:#=]?\s*(10\.[0-9]{4,9}/[-._;()/:a-z0-9]+)\s*(?:\(\s*HTTP\s+404\s+VERIFIED\s*\))?\s*[.;]?\s*$`)
	sessionRunnerNegativeNCTAfter      = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?NCT\s*[:#=]?\s*(NCT[0-9]{8})\s+(?:[-—–:]\s*)?(?:NOT\s+FOUND|UNVERIFIED)\s+IN\s+CLINICALTRIALS\.GOV\s*(?:\(\s*HTTP\s+404\s+VERIFIED\s*\))?\s*[.;]?\s*$`)
	sessionRunnerNegativeNCTBefore     = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?(?:NOT\s+FOUND|UNVERIFIED)\s+IN\s+CLINICALTRIALS\.GOV\s*(?:[-—–:]\s*)?NCT\s*[:#=]?\s*(NCT[0-9]{8})\s*(?:\(\s*HTTP\s+404\s+VERIFIED\s*\))?\s*[.;]?\s*$`)
	sessionRunnerNaturalNegativeClaim  = regexp.MustCompile(`(?i)(?:\bnot\s+found\b|\bno\s+(?:matching\s+)?(?:record|result|entry|study|trial)s?\b|\bdoes\s+not\s+exist\b|不存在|未(?:检索|查询|搜索|发现|找到)(?:到)?|无(?:匹配|对应|相关)?(?:的)?(?:记录|结果|条目|数据|试验))`)
	sessionRunnerUncertainNegative     = regexp.MustCompile(`(?i)(?:\bcannot\b|\bunable\b|\binsufficient\b|\bno\s+evidence\b|\bnot\s+necessarily\b|不能|无法|不足以|尚不能|未能确认|没有证据|缺乏证据|并非|并不(?:代表|意味着))`)
	sessionRunnerInlineCodePattern     = regexp.MustCompile("`([^`\\r\\n]+)`")
	sessionRunnerQuotedStringPattern   = regexp.MustCompile("[\\\"']([^\\\"'\\r\\n]{1,1000})[\\\"']")
	sessionRunnerWriteModePattern      = regexp.MustCompile(`(?i)^\s*,\s*(?:mode\s*=\s*)?["'][wax](?:[+bt]*)["']`)
	sessionRunnerOutputCallPattern     = regexp.MustCompile(`(?i)(?:save|savefig|imsave|imwrite|write_image|write_csv|write_json|to_csv|to_json|to_excel|to_parquet|dump|export)[a-z0-9_.]*\s*\(\s*$`)
)

var sessionRunnerAccessionPatterns = []struct {
	namespace string
	pattern   *regexp.Regexp
}{
	{namespace: "chembl", pattern: regexp.MustCompile(`(?i)\bCHEMBL[0-9]+\b`)},
	{namespace: "ensembl", pattern: regexp.MustCompile(`(?i)\bENSG[0-9]{11}(?:\.[0-9]+)?\b`)},
	{namespace: "clinvar", pattern: regexp.MustCompile(`(?i)\b(?:RCV|VCV)[0-9]+(?:\.[0-9]+)?\b`)},
	{namespace: "dbsnp", pattern: regexp.MustCompile(`(?i)\brs[0-9]+\b`)},
	{namespace: "refseq", pattern: regexp.MustCompile(`(?i)\b(?:NM|NR|NC|NG|NT|NW|NZ|XM|XR|XP|YP|WP)_[0-9]+(?:\.[0-9]+)?\b`)},
	{namespace: "geo", pattern: regexp.MustCompile(`(?i)\b(?:GSE|GSM|GPL)[0-9]+\b`)},
	{namespace: "sra", pattern: regexp.MustCompile(`(?i)\b(?:SR|ER|DR)[RSPX][0-9]+\b`)},
	{namespace: "pride", pattern: regexp.MustCompile(`(?i)\bPXD[0-9]+\b`)},
}
