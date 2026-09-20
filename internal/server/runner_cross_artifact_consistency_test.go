package server

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionRunnerCrossArtifactConsistencyNeedsNoAuthorityWithoutProducedArtifacts(t *testing.T) {
	for name, commits := range map[string][]transcriptstore.ArtifactReferenceInput{
		"empty":      nil,
		"cited only": {{ArtifactID: "artifact-a", VersionID: "version-a", Relation: transcriptstore.ArtifactRelationCited}},
	} {
		t.Run(name, func(t *testing.T) {
			failures, err := (*Server)(nil).validateSessionRunnerCrossArtifactConsistency(context.Background(), "", commits)
			if err != nil || len(failures) != 0 {
				t.Fatalf("failures=%#v err=%v", failures, err)
			}
		})
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsMissingReferenceAndEnvironmentConflict(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	save := func(id, name, content string) transcriptstore.ArtifactReferenceInput {
		t.Helper()
		artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: name, Kind: "text/plain",
			Content: []byte(content), CreatedBy: "runner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		save("artifact-env", "env_inventory.txt", "numpy OK 1.0\nseaborn MISSING No module named seaborn\n"),
		save("artifact-report", "verification_report.md", "See outputs/validation.json.\nseaborn 0.13.2\n"),
	}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerCrossArtifactConsistency(
		context.Background(), "project-a", commits,
	)
	want := []string{
		"environment_claim_conflict:seaborn in verification_report.md",
		"missing_artifact_reference:validation.json in verification_report.md",
	}
	if err != nil || !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v err=%v", failures, want, err)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAcceptsPublishedReferenceAndMissingDisclosure(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	save := func(id, name, content string) transcriptstore.ArtifactReferenceInput {
		t.Helper()
		artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: name, Kind: "text/plain",
			Content: []byte(content), CreatedBy: "runner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		save("artifact-env", "env_inventory.txt", "seaborn MISSING No module named seaborn\n"),
		save("artifact-validation", "validation.json", "{\"ok\":true}\n"),
		save("artifact-report", "verification_report.md", "See outputs/validation.json; seaborn is missing.\n"),
	}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerCrossArtifactConsistency(
		context.Background(), "project-a", commits,
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("failures=%#v err=%v", failures, err)
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsFinalIdentityThatOmitsAuthoritativeSourceArtifact(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	artifact, version, err := fixture.store.WriteArtifactVersion(
		context.Background(), workspace.WriteArtifactVersionInput{
			ArtifactID: "artifact-authoritative-structure", ProjectID: fixture.stream.ProjectID,
			Name: "9V09.cif", ContentType: "chemical/x-mmcif",
			Content: bytes.NewBufferString("data_9V09\n"), MaxBytes: 1 << 20,
			CreatedBy: fixture.claim.RunnerID, RootFrameID: fixture.stream.RootFrameID,
			FrameID: fixture.stream.FrameID, Language: agentPublicScientificArtifactLanguage,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	failures, err := fixture.server.validateSessionRunnerCrossArtifactConsistency(
		context.Background(), fixture.stream.ProjectID, commits,
		"The selected structure is PDB 8D7U.",
	)
	want := []string{"authoritative_artifact_reference_conflict:accession:pdb expected=accession:pdb:9V09 actual=accession:pdb:8D7U"}
	if err != nil || !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v err=%v", failures, want, err)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAllowsAuthoritativeIdentityWithComparators(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	artifact, version, err := fixture.store.WriteArtifactVersion(
		context.Background(), workspace.WriteArtifactVersionInput{
			ArtifactID: "artifact-authoritative-structure", ProjectID: fixture.stream.ProjectID,
			Name: "9V09.cif", ContentType: "chemical/x-mmcif",
			Content: bytes.NewBufferString("data_9V09\n"), MaxBytes: 1 << 20,
			CreatedBy: fixture.claim.RunnerID, RootFrameID: fixture.stream.RootFrameID,
			FrameID: fixture.stream.FrameID, Language: agentPublicScientificArtifactLanguage,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	failures, err := fixture.server.validateSessionRunnerCrossArtifactConsistency(
		context.Background(), fixture.stream.ProjectID, commits,
		"The selected structure is PDB 9V09; PDB 8D7U is a historical comparator.",
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("failures=%#v err=%v", failures, err)
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsNumericTableMismatch(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	save := func(id, name, kind, content string) transcriptstore.ArtifactReferenceInput {
		t.Helper()
		artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: name, Kind: kind,
			Content: []byte(content), CreatedBy: "runner",
		})
		if err != nil {
			t.Fatal(err)
		}
		return transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		save("artifact-table", "scores.csv", "text/csv", "形态,类型,物理稳定性,综合得分\nA,无水I,100,0.753232\nF,无水III,0,0.360977\n"),
		save("artifact-report", "decision_report.md", "text/markdown", "| 形态 | 类型 | 物理稳定性 | 综合得分 |\n|---|---|---:|---:|\n| A | 无水I | 100 | 0.753 |\n| F | 无水III | 30 | 0.361 |\n"),
		save("artifact-validation", "validation.json", "application/json", `{"overall_pass":true,"errors":[]}`),
	}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerCrossArtifactConsistency(
		context.Background(), "project-a", commits,
	)
	want := []string{"numeric_table_mismatch:scores.csv<->decision_report.md key=F column=物理稳定性 values=0|30"}
	if err != nil || !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v err=%v", failures, want, err)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAcceptsRoundedNumericTables(t *testing.T) {
	left := runnerCrossArtifactSnapshot{name: "scores.csv", text: "form,type,score,stability\nA,I,0.753232,100\nF,III,0.360977,30\n"}
	right := runnerCrossArtifactSnapshot{name: "report.md", text: "| form | type | score | stability |\n|---|---|---:|---:|\n| A | I | 0.753 | 100 |\n| F | III | 0.361 | 30 |\n"}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{left, right}); len(failures) != 0 {
		t.Fatalf("rounded values were rejected: %#v", failures)
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsTransposedReportMismatch(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "drug_comparison.csv",
		text: "药物,半衰期 (小时),\"客观缓解率 (ORR, %)\",\"中位无进展生存期 (PFS, 月)\",\"中位总生存期 (OS, 月)\"\n" +
			"阿达格拉西布,24.0,43,8.3,14.1\n" +
			"索托拉西布,5.6,37,6.5,12.5\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| 参数 | 阿达格拉西布 | 索托拉西布 |\n|---|---:|---:|\n" +
			"| 半衰期 (小时) | 24.0 小时 | 5.6 小时 |\n" +
			"| 客观缓解率 (ORR) | 42.9% | 37.1% |\n" +
			"| 中位无进展生存期 (PFS) | 6.5 个月 | 5.6 个月 |\n" +
			"| 中位总生存期 (OS) | 12.6 个月 | 12.5 个月 |\n",
	}
	want := []string{
		"numeric_transposed_table_mismatch:drug_comparison.csv<->report.md entity=阿达格拉西布 metric=中位无进展生存期 (PFS) values=8.3|6.5 个月",
		"numeric_transposed_table_mismatch:drug_comparison.csv<->report.md entity=索托拉西布 metric=中位无进展生存期 (PFS) values=6.5|5.6 个月",
		"numeric_transposed_table_mismatch:drug_comparison.csv<->report.md entity=阿达格拉西布 metric=中位总生存期 (OS) values=14.1|12.6 个月",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data}); !reflect.DeepEqual(failures, want) {
		t.Fatalf("transposed table failures=%#v want=%#v", failures, want)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAcceptsConsistentTransposedReport(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "drug_comparison.csv",
		text: "drug,half_life,orr,pfs\nA,24.0,42.9,8.3\nB,5.6,37.1,6.5\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| parameter | A | B |\n|---|---:|---:|\n" +
			"| half_life (hours) | 24.0 hours | 5.6 hours |\n" +
			"| orr (%) | 42.9% | 37.1% |\n" +
			"| pfs (months) | 8.3 months | 6.5 months |\n",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data}); len(failures) != 0 {
		t.Fatalf("consistent transposed tables were rejected: %#v", failures)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAcceptsRoundedNumericRowIdentity(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "exposure_simulation.csv",
		text: "dose,total_dose,auc,cmax\n0.013514,0.81,0.041,0.0027\n0.135135,8.11,0.405,0.0270\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| dose | total_dose | auc | cmax |\n|---:|---:|---:|---:|\n| 0.0135 | 0.81 | 0.041 | 0.0027 |\n| 0.1351 | 8.11 | 0.405 | 0.0270 |\n",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data}); len(failures) != 0 {
		t.Fatalf("rounded numeric row identities were rejected: %#v", failures)
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsAmbiguousRoundedNumericRowIdentity(t *testing.T) {
	if row, found := runnerCrossArtifactApproximateIdentityRow(
		[]string{"0.0135", "1.0"},
		[][]string{{"0.01351", "1.0"}, {"0.01354", "2.0"}},
		"dose", nil, 0,
	); found || row != nil {
		t.Fatalf("ambiguous rounded identity matched row=%#v", row)
	}
}

func TestRunnerEvidenceProvenanceRejectsCategoryWithoutStableLocator(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "evidence.csv",
		text: "machine_id,claim,source_type,source\n1,dose rule,label,FDA label\n2,scenario value,assumption,model-defined\n3,trial outcome,trial,PMID: 12345678\n",
	}
	want := []string{"evidence_source_locator_missing:evidence.csv row=2 source_type=label"}
	if failures := runnerEvidenceProvenanceFailures(snapshot); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerEvidenceProvenanceIgnoresImmutableComputationManifest(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "out/docking_components.csv",
		text: "component_kind,candidate_id,rank,pose_rank,chain_id,residue_name,residue_number,best_affinity_kcal_mol,affinity_kcal_mol,sample_run,source_mode,reference_centroid_distance_angstrom,reference_axis_cosine,source\n" +
			"protein,,,,A,,,,,,,,,selected_receptor.pdb\n" +
			"reference_ligand,A1J8Z,0,0,Z,REF,1,,,,,,,A1J8Z:A:303\n" +
			"docked_ligand,93597066,1,1,Z,D01,101,-6.831,-6.831,1,1,3.18,0.05,pose-0001-run-01.pdbqt\n",
	}
	if _, _, recognized := runnerEvidenceLedgerRecords(snapshot); recognized {
		t.Fatal("quantitative execution manifest was classified as an evidence ledger")
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("quantitative execution manifest received evidence findings: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceAcceptsResolvableSourceColumns(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "source_evidence.tsv",
		text: "id\tclaim\tsource_type\tsource_url\tregulatory_id\n1\tlabel rule\tlabel\thttps://www.accessdata.fda.gov/example.pdf\t\n2\tapproval\tregulatory\t\tNDA 123456\n3\tunknown boundary\tunknown\t\t\n",
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("valid provenance rejected: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceAcceptsStableSourceIDWithoutDuplicateURL(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "evidence.csv",
		text: "claim,source_type,source_id\n" +
			"registered outcome,clinical_trial,NCT04616014\n" +
			"published result,publication,doi:10.1000/example\n",
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("stable source_id rejected: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceAcceptsLocalizedSourceURLColumns(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "evidence_list.csv",
		text: "技术类型,证据类型,标识符,标题,来源,来源URL\n" +
			"口服肽递送,临床试验,NCT05142228,registered trial,ClinicalTrials.gov,https://clinicaltrials.gov/study/NCT05142228\n" +
			"离子液体,专利,US-2023040805-A1,delivery patent,PubChem,https://pubchem.ncbi.nlm.nih.gov/patent/US-2023040805-A1\n" +
			"脂质纳米载体,综述,391786520,review,ResearchGate,https://www.researchgate.net/publication/391786520\n",
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("localized provenance columns were rejected: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceUsesCombinedDOIURLColumnInsteadOfSourceLabel(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "研发证据表.csv",
		text: "证据类别,标题,来源,发表日期,证据要点,DOI/URL\n" +
			"临床试验,Primary trial,Nature Medicine,2026-04-01,Reported clinical outcome,https://example.test/trial\n" +
			"临床试验,Unresolved trial,ClinicalTrials.gov,2026-05-01,Status requires a locator,\n",
	}
	want := []string{"evidence_source_locator_missing:研发证据表.csv row=3 source_type=evidence"}
	if failures := runnerEvidenceProvenanceFailures(snapshot); !reflect.DeepEqual(failures, want) {
		t.Fatalf("combined DOI/URL column mapping failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerEvidenceProvenanceAcceptsChineseReferenceColumn(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "candidate_evidence.csv",
		text: "项目,关键结果,参考文献\n" +
			"候选A,公开来源支持的结论,https://example.test/public-record\n" +
			"候选B,另一项公开结论,doi:10.1000/example\n",
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("Chinese reference column was rejected: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceAcceptsPlainChineseLinkColumn(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "traceable_sources.csv",
		text: "来源类型,标识符/编号,标题,来源,链接\n" +
			"专利,CN115698003B,GLP-1 agonist,Google Patents,https://patents.google.com/patent/CN115698003B/en\n",
	}
	if failures := runnerEvidenceProvenanceFailures(snapshot); len(failures) != 0 {
		t.Fatalf("plain Chinese link column was rejected: %#v", failures)
	}
}

func TestRunnerEvidenceProvenanceRecognizesSchemaWithoutEnglishFilename(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "研发证据表.csv",
		text: "证据类别,来源,关键结论,标识\n" +
			"领域综述,Expert Opin Ther Pat,九个候选进入临床,doi:10.1080/example\n" +
			"临床管线,企业官网,候选处于一期,CompanyName\n" +
			"专利,Google Patents,化合物权利要求,AU2021390194B2\n",
	}
	want := []string{"evidence_source_locator_missing:研发证据表.csv row=3 source_type=evidence"}
	if failures := runnerEvidenceProvenanceFailures(snapshot); !reflect.DeepEqual(failures, want) {
		t.Fatalf("schema-based provenance failures=%#v want=%#v", failures, want)
	}
}

func TestSessionRunnerCrossArtifactConsistencyMatchesBilingualScientificHeaders(t *testing.T) {
	data := runnerCrossArtifactSnapshot{name: "topsis.csv", text: "form,type,topsis_score,rank\nA,无水晶型I,0.5401729595,1\nE,无定形,0.5366647626,2\n"}
	report := runnerCrossArtifactSnapshot{name: "report.md", text: "| 形态 | 类型 | TOPSIS综合得分 | 排名 |\n|---|---|---:|---:|\n| A | 无水晶型I | 0.714 | 1 |\n| E | 无定形 | 0.386 | 2 |\n"}
	failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data})
	want := []string{
		"numeric_table_mismatch:topsis.csv<->report.md key=A column=topsis_score values=0.5401729595|0.714",
		"numeric_table_mismatch:topsis.csv<->report.md key=E column=topsis_score values=0.5366647626|0.386",
	}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestSessionRunnerCrossArtifactConsistencyUsesMinimalUniqueCompositeRowKey(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "sensitivity.csv",
		text: "parameter,change_percent,relative_change_k\nactivation_energy,10,-96.1795\nactivation_energy,-10,2517.4857\nhumidity_coefficient,10,3.8212\nhumidity_coefficient,-10,-3.6806\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| parameter | change(%) | relative change (%) |\n|---|---:|---:|\n| activation_energy | +10% | -39.5% |\n| activation_energy | -10% | +76.1% |\n| humidity_coefficient | +10% | +13.3% |\n| humidity_coefficient | -10% | -11.8% |\n",
	}
	want := []string{
		"numeric_table_mismatch:sensitivity.csv<->report.md key=activation_energy/change_percent=10 column=relative_change_k values=-96.1795|-39.5%",
		"numeric_table_mismatch:sensitivity.csv<->report.md key=activation_energy/change_percent=-10 column=relative_change_k values=2517.4857|+76.1%",
		"numeric_table_mismatch:sensitivity.csv<->report.md key=humidity_coefficient/change_percent=10 column=relative_change_k values=3.8212|+13.3%",
		"numeric_table_mismatch:sensitivity.csv<->report.md key=humidity_coefficient/change_percent=-10 column=relative_change_k values=-3.6806|-11.8%",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data}); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestSessionRunnerCrossArtifactConsistencyDoesNotInventIdentityAcrossTranslatedRows(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "sensitivity.csv",
		text: "parameter,change_percent,relative_change_k\nactivation_energy,10,-96.1795\nactivation_energy,-10,2517.4857\nhumidity_coefficient,10,3.8212\nhumidity_coefficient,-10,-3.6806\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| 参数 | 变化幅度 | 降解速率相对变化 |\n|---|---:|---:|\n| 活化能 | +10% | -39.5% |\n| 活化能 | -10% | +76.1% |\n| 湿度系数 | +10% | +13.3% |\n| 湿度系数 | -10% | -11.8% |\n",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data}); len(failures) != 0 {
		t.Fatalf("unbound translated row labels were treated as the same facts: %#v", failures)
	}
}

func TestSessionRunnerCrossArtifactConsistencyAllowsIndependentSiblingTables(t *testing.T) {
	response := runnerCrossArtifactSnapshot{
		name: "response_de.csv",
		text: "group,gene,score,p_value\nresponder,IFNG,4.2,0.001\nnon_responder,VEGFA,3.1,0.004\n",
	}
	treatment := runnerCrossArtifactSnapshot{
		name: "treatment_de.csv",
		text: "group,gene,score,p_value\npre,CCR7,2.8,0.003\npost,GZMB,5.4,0.0001\n",
	}
	if failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{response, treatment}); len(failures) != 0 {
		t.Fatalf("independent sibling tables were treated as conflicting: %#v", failures)
	}
}

func TestSessionRunnerCrossArtifactConsistencyRejectsBilingualScenarioRankMismatch(t *testing.T) {
	data := runnerCrossArtifactSnapshot{
		name: "scenario_scores.csv",
		text: "form,score_pessimistic,rank_pessimistic,score_neutral,rank_neutral,score_optimistic,rank_optimistic\nA,0.9,1,0.9,1,0.9,1\nF,0.8,2,0.8,2,0.8,2\nE,0.7,3,0.7,3,0.7,3\n",
	}
	report := runnerCrossArtifactSnapshot{
		name: "report.md",
		text: "| 形态 | 悲观情景排名 | 中性情景排名 | 乐观情景排名 |\n|---|---:|---:|---:|\n| A | 1 | 1 | 1 |\n| F | 3 | 2 | 2 |\n| E | 3 | 3 | 2 |\n",
	}

	failures := runnerCrossArtifactTableFailures([]runnerCrossArtifactSnapshot{report, data})
	want := []string{
		"numeric_table_mismatch:scenario_scores.csv<->report.md key=F column=rank_pessimistic values=2|3",
		"numeric_table_mismatch:scenario_scores.csv<->report.md key=E column=rank_optimistic values=3|2",
	}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerCrossArtifactRankGateRequiresScenarioScores(t *testing.T) {
	table := runnerCrossArtifactTable{
		source:  "scenario.csv",
		headers: []string{"form", "rank_pessimistic", "rank_neutral", "rank_optimistic"},
		rows:    [][]string{{"A", "1", "1", "1"}, {"F", "4", "2", "2"}},
	}
	want := []string{
		"rank_score_pair_missing:scenario.csv rank=rank_pessimistic score=score_pessimistic",
		"rank_score_pair_missing:scenario.csv rank=rank_neutral score=score_neutral",
		"rank_score_pair_missing:scenario.csv rank=rank_optimistic score=score_optimistic",
	}
	if failures := runnerCrossArtifactRankFailures(table); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerCrossArtifactRankGateRecomputesRankFromScores(t *testing.T) {
	table := runnerCrossArtifactTable{
		source:  "scenario.csv",
		headers: []string{"form", "score_pessimistic", "rank_pessimistic"},
		rows:    [][]string{{"A", "0.8", "1"}, {"F", "0.7", "4"}, {"E", "0.6", "3"}},
	}
	want := []string{
		"rank_score_mismatch:scenario.csv key=F columns=score_pessimistic/rank_pessimistic score=0.7 rank=4 expected_rank=2",
	}
	if failures := runnerCrossArtifactRankFailures(table); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerMachineValidationRejectsFalseChecksAndErrors(t *testing.T) {
	failures := runnerMachineValidationFailures("validation.json", `{
		"input_fidelity_passed": true,
		"cross_artifact_consistency_passed": false,
		"errors": ["report mismatch"]
	}`)
	want := []string{
		"machine_validation_false_check:validation.json path=cross_artifact_consistency_passed",
		"machine_validation_reported_failure:validation.json path=errors",
	}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
}

func TestRunnerMachineValidationRequiresPassingCheck(t *testing.T) {
	if failures := runnerMachineValidationFailures("validation.json", `{"record_count":6,"errors":[]}`); !reflect.DeepEqual(
		failures, []string{"machine_validation_missing_passing_check:validation.json"},
	) {
		t.Fatalf("failures=%#v", failures)
	}
	if failures := runnerMachineValidationFailures("validation.json", `{"overall_pass":true,"errors":[]}`); len(failures) != 0 {
		t.Fatalf("valid machine record rejected: %#v", failures)
	}
	if failures := runnerMachineValidationFailures("validation.json", `{"validation_status":"completed","errors":[]}`); len(failures) != 0 {
		t.Fatalf("completed machine status rejected: %#v", failures)
	}
	if failures := runnerMachineValidationFailures("validation.json", `{"validated":true,"meets_requirements":true,"errors":[]}`); len(failures) != 0 {
		t.Fatalf("semantic passing checks were rejected: %#v", failures)
	}
	if failures := runnerMachineValidationFailures("validation.json", `[{"check_name":"source integrity","passed":true},{"check_name":"cross artifact consistency","passed":true}]`); len(failures) != 0 {
		t.Fatalf("non-empty validation record array was rejected: %#v", failures)
	}
	if failures := runnerMachineValidationFailures("validation.json", `[{"check_name":"source integrity","passed":false}]`); !reflect.DeepEqual(
		failures, []string{"machine_validation_false_check:validation.json path=[0].passed", "machine_validation_missing_passing_check:validation.json"},
	) {
		t.Fatalf("failed validation array failures=%#v", failures)
	}
	for _, invalid := range []string{`[]`, `{}`, `true`, `"passed"`} {
		if failures := runnerMachineValidationFailures("validation.json", invalid); !reflect.DeepEqual(
			failures, []string{"machine_validation_invalid_root:validation.json"},
		) {
			t.Fatalf("invalid root %s failures=%#v", invalid, failures)
		}
	}
	if failures := runnerMachineValidationFailures("validation.json", `{"validation_status":"incomplete","errors":[]}`); len(failures) == 0 {
		t.Fatal("incomplete machine status was accepted")
	}
	if failures := runnerMachineValidationFailures("validation.json", `{
		"input_fidelity":true,"missing_values_preserved":true,
		"cross_artifact_consistency":true,"source_integrity":true,
		"deterministic_reproducibility":true,"data_to_chart_consistency":true,
		"visual_quality":true,"process_cleanup":true,"errors":[]
	}`); len(failures) != 0 {
		t.Fatalf("standard quality checks were rejected: %#v", failures)
	}
	if failures := runnerMachineValidationFailures("generation_validation.json", `{
		"status":"passed","overall_pass":true,"errors":[],
		"checks":{"exact_count":true,"unique_canonical_smiles":true,
		"parent_excluded":true,"required_core_preserved":true,"three_dimensional_sdf":true}
	}`); len(failures) != 0 {
		t.Fatalf("canonical generation validation was rejected: %#v", failures)
	}
}

func TestMoleculeSetRecordCountFailuresRequireMatchingSDFAndSMILESCompanions(t *testing.T) {
	counts := map[string]map[string]int{
		"matching": {"sdf": 20, "smi": 20},
		"broken":   {"sdf": 20, "smi": 0},
		"sdf-only": {"sdf": 5},
	}
	want := []string{"molecule_set_record_count_mismatch:broken sdf=20 smiles=0"}
	if got := moleculeSetRecordCountFailures(counts); !reflect.DeepEqual(got, want) {
		t.Fatalf("molecule-set failures=%#v want=%#v", got, want)
	}
}

func TestRunnerCrossArtifactTemplateGateRejectsUnrenderedJinja(t *testing.T) {
	content := "| form | score |\n|---|---:|\n{% for row in rows %}| {{row.form}} | {{row.score}} |{% endfor %}\n"
	want := []string{"unresolved_template_marker:report.md"}
	if failures := runnerCrossArtifactTemplateFailures("report.md", content); !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures=%#v want=%#v", failures, want)
	}
	if failures := runnerCrossArtifactTemplateFailures("report.md", "![plot]({{artifact:123e4567-e89b-12d3-a456-426614174000}})"); len(failures) != 0 {
		t.Fatalf("valid artifact placeholder rejected: %#v", failures)
	}
	if failures := runnerCrossArtifactTemplateFailures("report.md", "{{artifact:art_123e4567-e89b-12d3-a456-426614174000}}"); !reflect.DeepEqual(
		failures, []string{"malformed_artifact_placeholder:report.md value=art_123e4567-e89b-12d3-a456-426614174000"},
	) {
		t.Fatalf("malformed artifact placeholder failures=%#v", failures)
	}
}

func TestRunnerCrossArtifactTemplateGateRejectsUnrenderedFormatFields(t *testing.T) {
	for _, content := range []string{
		"MRSD was {mr_sd:.4f} mg/kg.",
		"Result: {result}",
	} {
		want := []string{"unresolved_template_marker:report.md"}
		if failures := runnerCrossArtifactTemplateFailures("report.md", content); !reflect.DeepEqual(failures, want) {
			t.Fatalf("content %q: got %#v want %#v", content, failures, want)
		}
	}
	for _, content := range []string{
		"A_{UC} is mathematical notation.",
		"$$ MABEL_{molar} = \\frac{free\\ IC_{50} \\times V_d}{F} $$",
		"Inline $A = \\frac{Dose}{CL}$ remains rendered math.",
		"JSON example: {\"status\": \"ok\"}",
		"IUPAC name: 3-methyl-~{N}-(2-phenylethyl)-triazolopyridazin-6-amine.",
		"{{artifact:123e4567-e89b-12d3-a456-426614174000}}",
	} {
		if failures := runnerCrossArtifactTemplateFailures("report.md", content); len(failures) != 0 {
			t.Fatalf("content %q was rejected: %#v", content, failures)
		}
	}
}

func TestSessionRunnerCompletionSkipsDeletedHistoricalProducedArtifact(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	commit := transcriptstore.ArtifactReferenceInput{
		ArtifactID: "123e4567-e89b-12d3-a456-426614174001",
		VersionID:  "123e4567-e89b-12d3-a456-426614174002",
		Relation:   transcriptstore.ArtifactRelationProduced,
	}
	server := &Server{workspaceStore: store}
	if failures, err := server.validateSessionRunnerCrossArtifactConsistency(context.Background(), "project-a", []transcriptstore.ArtifactReferenceInput{commit}); err != nil || len(failures) != 0 {
		t.Fatalf("deleted historical cross-artifact failures=%#v err=%v", failures, err)
	}
	if failures, err := server.validateSessionRunnerScientificArtifacts(context.Background(), "project-a", []transcriptstore.ArtifactReferenceInput{commit}, ""); err != nil || len(failures) != 0 {
		t.Fatalf("deleted historical scientific failures=%#v err=%v", failures, err)
	}
	if _, err := server.validateSessionRunnerScientificArtifacts(
		context.Background(), "project-a", []transcriptstore.ArtifactReferenceInput{commit},
		"[deleted]({{artifact:123e4567-e89b-12d3-a456-426614174002}})",
	); err == nil {
		t.Fatal("final answer explicitly referencing a deleted artifact version was accepted")
	}
	if result, err := server.validateSessionRunnerResearchArtifactSelection(context.Background(), "project-a", []transcriptstore.ArtifactReferenceInput{commit}); err != nil || len(result.Failures) != 0 {
		t.Fatalf("deleted historical research selection=%#v err=%v", result, err)
	}
	if ledger, err := server.sessionRunnerResearchArtifactManifestLedger("project-a", []transcriptstore.ArtifactReferenceInput{commit}); err != nil || ledger == "" {
		t.Fatalf("deleted historical research ledger=%q err=%v", ledger, err)
	}
}
