package server

import (
	"encoding/json"
	"encoding/xml"
	"net/url"
	"strings"
)

func addVerifiedSessionRunnerPubMedReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	result, ok := envelope.(map[string]any)
	if !ok {
		return
	}
	if nested, nestedOK := result["result"].(map[string]any); nestedOK {
		result = nested
	}
	requested := sessionRunnerPubMedRequestIdentifiers(parsed)
	if len(requested) == 0 {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	observed := map[string]struct{}{}
	for _, match := range sessionRunnerPubMedXMLPattern.FindAllStringSubmatch(body, -1) {
		if len(match) == 2 {
			observed[match[1]] = struct{}{}
		}
	}
	for _, match := range sessionRunnerPubMedUIDPattern.FindAllStringSubmatch(body, -1) {
		if len(match) == 2 {
			observed[match[1]] = struct{}{}
		}
	}
	for _, listMatch := range sessionRunnerPubMedUIDsPattern.FindAllStringSubmatch(body, -1) {
		if len(listMatch) != 2 {
			continue
		}
		for _, match := range sessionRunnerQuotedPMIDPattern.FindAllStringSubmatch(listMatch[1], -1) {
			if len(match) == 2 {
				observed[match[1]] = struct{}{}
			}
		}
	}
	for identifier := range requested {
		if _, found := observed[identifier]; found {
			addSessionRunnerReference(target, "pmid", "", identifier)
		}
	}
	addVerifiedSessionRunnerPubMedRecordReferences(target, requested, body)
}

func addVerifiedSessionRunnerPubMedRecordReferences(target, requested map[string]struct{}, body string) {
	type xmlIdentifier struct {
		Type  string `xml:"IdType,attr"`
		Value string `xml:",chardata"`
	}
	type xmlLocation struct {
		Type  string `xml:"EIdType,attr"`
		Value string `xml:",chardata"`
	}
	var xmlResponse struct {
		Articles []struct {
			MedlineCitation struct {
				PMID    string `xml:"PMID"`
				Article struct {
					Locations []xmlLocation `xml:"ELocationID"`
				} `xml:"Article"`
			} `xml:"MedlineCitation"`
			PubmedData struct {
				Identifiers []xmlIdentifier `xml:"ArticleIdList>ArticleId"`
			} `xml:"PubmedData"`
		} `xml:"PubmedArticle"`
	}
	if xml.Unmarshal([]byte(body), &xmlResponse) == nil {
		for _, article := range xmlResponse.Articles {
			pmid := strings.TrimSpace(article.MedlineCitation.PMID)
			if _, found := requested[pmid]; !found || !sessionRunnerPMIDExactPattern.MatchString(pmid) {
				continue
			}
			addSessionRunnerReference(target, "pmid", "", pmid)
			for _, identifier := range article.PubmedData.Identifiers {
				if strings.EqualFold(strings.TrimSpace(identifier.Type), "doi") {
					addSessionRunnerReference(target, "doi", "", identifier.Value)
				}
			}
			for _, location := range article.MedlineCitation.Article.Locations {
				if strings.EqualFold(strings.TrimSpace(location.Type), "doi") {
					addSessionRunnerReference(target, "doi", "", location.Value)
				}
			}
		}
	}

	var jsonResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(body), &jsonResponse) != nil {
		return
	}
	for pmid := range requested {
		raw, found := jsonResponse.Result[pmid]
		if !found {
			continue
		}
		var item struct {
			UID         string `json:"uid"`
			DOI         string `json:"doi"`
			ELocationID string `json:"elocationid"`
			ArticleIDs  []struct {
				Type  string `json:"idtype"`
				Value string `json:"value"`
			} `json:"articleids"`
		}
		if json.Unmarshal(raw, &item) != nil || strings.TrimSpace(item.UID) != pmid {
			continue
		}
		addSessionRunnerReference(target, "pmid", "", pmid)
		addSessionRunnerReference(target, "doi", "", sessionRunnerPubMedDOIValue(item.DOI))
		addSessionRunnerReference(target, "doi", "", sessionRunnerPubMedDOIValue(item.ELocationID))
		for _, identifier := range item.ArticleIDs {
			if strings.EqualFold(strings.TrimSpace(identifier.Type), "doi") {
				addSessionRunnerReference(target, "doi", "", sessionRunnerPubMedDOIValue(identifier.Value))
			}
		}
	}
}

func sessionRunnerPubMedDOIValue(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "doi:") {
		value = strings.TrimSpace(value[len("doi:"):])
	}
	return value
}

func addVerifiedSessionRunnerEuropePMCReferences(target map[string]struct{}, envelope any, parsed *url.URL) {
	result, ok := envelope.(map[string]any)
	if !ok {
		return
	}
	if nested, nestedOK := result["result"].(map[string]any); nestedOK {
		result = nested
	}
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
		normalizedSessionRunnerEvidenceHost(parsed) != "www.ebi.ac.uk" ||
		!strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/europepmc/webservices/rest/search") ||
		!strings.EqualFold(strings.TrimSpace(parsed.Query().Get("format")), "json") {
		return
	}
	requested, ok := exactSessionRunnerEuropePMCQueryIdentifiers(parsed.Query().Get("query"))
	if !ok {
		return
	}
	body := firstNonEmpty(stringValue(result["body"]), stringValue(result["result"]), stringValue(result["content"]))
	var response struct {
		Request struct {
			QueryString string `json:"queryString"`
		} `json:"request"`
		ResultList struct {
			Results []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
				PMID   string `json:"pmid"`
				DOI    string `json:"doi"`
			} `json:"result"`
		} `json:"resultList"`
	}
	if json.Unmarshal([]byte(body), &response) != nil {
		return
	}
	responseRequested, ok := exactSessionRunnerEuropePMCQueryIdentifiers(response.Request.QueryString)
	if !ok || !sameSessionRunnerIdentifierSet(requested, responseRequested) {
		return
	}
	for _, item := range response.ResultList.Results {
		identifier := strings.TrimSpace(item.PMID)
		if !strings.EqualFold(strings.TrimSpace(item.Source), "MED") ||
			identifier != strings.TrimSpace(item.ID) || !sessionRunnerPMIDExactPattern.MatchString(identifier) {
			continue
		}
		if _, found := requested[identifier]; found {
			addSessionRunnerReference(target, "pmid", "", identifier)
			addSessionRunnerReference(target, "doi", "", item.DOI)
		}
	}
}

func exactSessionRunnerEuropePMCQueryIdentifiers(value string) (map[string]struct{}, bool) {
	value = strings.TrimSpace(value)
	if !sessionRunnerEuropePMCQueryPattern.MatchString(value) {
		return nil, false
	}
	identifiers := map[string]struct{}{}
	for _, match := range sessionRunnerEuropePMCIDPattern.FindAllStringSubmatch(value, -1) {
		if len(match) == 2 {
			identifiers[match[1]] = struct{}{}
		}
	}
	return identifiers, len(identifiers) > 0
}

func sameSessionRunnerIdentifierSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for identifier := range left {
		if _, found := right[identifier]; !found {
			return false
		}
	}
	return true
}
