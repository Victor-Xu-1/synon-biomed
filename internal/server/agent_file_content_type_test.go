package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestAgentFileContentTypeContract(t *testing.T) {
	for _, test := range []struct {
		name, content string
		invalid       bool
	}{
		{"structure.cif", "<!-- download complete -->", true},
		{"other.mmcif", "\xef\xbb\xbf<html>error</html>", true},
		{"samples.csv", "<!DOCTYPE html><html>error</html>", true},
		{"dataset.h5ad", "%PDF-1.7\n", true},
		{"report.pdf", "<html>not a PDF</html>", true},
		{"structure.cif", "data_example\n_entry.id example\n", false},
		{"samples.csv", "sample,value\na,2\n", false},
		{"report.pdf", "%PDF-1.7\n", false},
		{"report.html", "<html>intentional report</html>", false},
		{"notes.md", "<!-- intentional comment -->\n# Notes", false},
	} {
		t.Run(test.name+test.content[:1], func(t *testing.T) {
			root := t.TempDir()
			writeAgentSaveArtifactsFile(t, root, test.name, test.content)
			file, err := os.Open(filepath.Join(root, test.name))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Seek(1, 0); err != nil {
				t.Fatal(err)
			}
			err = validateAgentFileContentType(test.name, file)
			if errors.Is(err, errAgentFileContentTypeMismatch) != test.invalid {
				t.Fatalf("invalid=%t err=%v", test.invalid, err)
			}
			if offset, _ := file.Seek(0, 1); offset != 1 {
				t.Fatalf("cursor changed to %d", offset)
			}
		})
	}
}

func TestAgentFileContentTypeEditPreservesSourceAndAllowsSave(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const name = "structure.cif"
	const content = "data_example\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\n"
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, name, content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "source-write", 1, write)
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas:     []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()},
		hasToolSnapshot: true, suppressHooks: true,
	}
	rejected := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": name, "old_string": "", "new_string": "<!-- saved -->",
	})
	if rejected["code"] != "file_content_type_mismatch" || rejected["executed"] != false ||
		!strings.Contains(stringValue(rejected["recovery"]), "save it directly") {
		t.Fatalf("recovery=%#v", rejected)
	}
	actual, err := os.ReadFile(filepath.Join(fixture.projectPath, name))
	if err != nil || string(actual) != content {
		t.Fatalf("source changed: %q %v", actual, err)
	}
	fragment := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": name, "old_string": "", "new_string": "data_example\n_entry.id example\n",
	})
	if fragment["code"] != "invalid_file_structure" || fragment["executed"] != false {
		t.Fatalf("fragment overwrite=%#v", fragment)
	}
	actual, err = os.ReadFile(filepath.Join(fixture.projectPath, name))
	if err != nil || string(actual) != content {
		t.Fatalf("fragment replaced source: %q %v", actual, err)
	}
	input := map[string]any{"files": []any{name}, "language": "text", "human_description": "Saving source"}
	saved, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-preserved", input), fixture.identity, "save-preserved", input)
	if err != nil || len(agentSaveArtifactResults(t, saved)) != 1 {
		t.Fatalf("save=%#v err=%v", saved, err)
	}
	version := stringValue(agentSaveArtifactResults(t, saved)[0]["version_id"])
	valid := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": name, "old_string": "", "new_string": content + "# derived\n",
	})
	if valid["success"] != true {
		t.Fatalf("valid rewrite=%#v", valid)
	}
	// Files written by other tools still cross the same publication boundary.
	write = writeAgentSaveArtifactsFile(t, fixture.projectPath, name, "<html>error</html>")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "external-write", 2, write)
	input["version_of"] = map[string]any{name: version}
	result, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-invalid", input), fixture.identity, "save-invalid", input)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("invalid save=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["code"] != "file_content_type_mismatch" {
		t.Fatalf("errors=%#v", failures)
	}
	_, current, found, err := fixture.store.GetCurrentArtifactVersionMetadata(agentSavedArtifactID(fixture.stream.UID, name))
	if err != nil || !found || current.ID != version {
		t.Fatalf("version changed: %#v %v", current, err)
	}
}

func TestAgentFileContentTypeDownloadRejectsMisreportedHeader(t *testing.T) {
	root := t.TempDir()
	writeAgentSaveArtifactsFile(t, root, "data.cif", "<html>upstream error</html>")
	file, err := os.Open(filepath.Join(root, "data.cif"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	accepted, err := agentPublicScientificAcceptedTypes("data.cif")
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyAgentPublicScientificStagedContent(file, "data.cif", "chemical/x-cif", accepted)
	if !errors.Is(err, errAgentFileContentTypeMismatch) {
		t.Fatalf("misreported body accepted: %v", err)
	}
}
