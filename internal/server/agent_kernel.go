package server

import (
	"context"

	"errors"

	"path/filepath"

	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type agentKernelContext struct {
	access       workspace.KernelFrameAccess
	workspaceDir string
}

const (
	agentKernelManagedPythonEnvironment     = "synon-biomed-python"
	defaultAgentKernelForegroundWaitTimeout = 10 * time.Minute
	// A zero execution timeout means no wall-clock kill. Foreground waiting may
	// still detach into durable supervision, while user cancellation and runtime
	// drain remain explicit lifecycle commands.
	defaultAgentKernelExecutionTimeout   time.Duration = 0
	softwareRuntimeExecutionTimeoutGrace               = 30 * time.Second
	maxAgentKernelForegroundWaitSeconds                = int64(2000000)
	agentKernelPythonDescription                       = "Execute standard Python analysis code in the persistent Synon kernel; variables and cwd persist for the same task and environment. This is not IPython or Jupyter, so do not use %run or other magics. Load its Skill before a specialized workflow, make sure every non-standard import is present in the chosen environment, and inspect the installed API before using unfamiliar symbols. Use manage_environments and manage_packages for software; never run pip or conda in a cell. A successful package mutation returns an immutable successor: discard the source environment name and use the returned environment.name in every subsequent Python call. Use Python for analysis, not public-file transfer: complete public datasets, archives, structures, and PDFs must come from download_public_scientific_file after source discovery. A failed cell ends that execution unit: inspect the actual error or interface, preserve successful outputs, and make a materially corrected attempt. If the same tactic still fails, change the technical tactic within the selected implementation instead of abandoning the objective; changing only foreground/background is not a correction. Let long computation continue or run in the background with concise progress output. For portable tabular output, write CSV with csv or DataFrame.to_csv and build any short Markdown table directly; DataFrame.to_markdown requires the optional tabulate package and must not be called unless that import already succeeds. For a fresh-process check, save and run `python script.py` with Bash in the same environment. Resolve ltr-* or other immutable data with host.artifact_path(version_id); /api/artifacts/ is a display URL, not a filesystem path. Read important outputs back and save useful tables, figures, data, or reports for the user."
	agentKernelRDescription                            = "Execute bounded R analysis code in the persistent analysis kernel for this agent Frame. Variables, functions, loaded libraries, and cwd persist across calls. Load a matching Skill before specialized libraries and inspect the installed help once when no Skill exists. A failed cell ends its execution unit; inspect before one new cell and close the path after a second failure. Use exact task-relative workspace files as inputs. Immutable {{artifact:VERSION_ID}} markers and /api/artifacts/ URLs are presentation references, not filesystem paths, and remain unchanged when written into reports. This kernel has no host bridge or MCP; use repl for allowed host operations. Use manage_environments and manage_packages as the single software-management path. Save user-facing outputs with save_artifacts."
	agentKernelBashDescription                         = "Execute one bounded Bash step in the selected verified managed environment and task workspace. Use Bash for command-line programs and project-local file operations, not package installation: all environment and package changes must use manage_environments or manage_packages. Use exact task-relative workspace files as inputs. Immutable {{artifact:VERSION_ID}} markers and /api/artifacts/ URLs are presentation references, not filesystem paths, and remain unchanged when written into reports. HTTPS egress is available only through the host proxy for user-approved domains; direct sockets and SSH are blocked. The shell is resource-bounded and durably supervised. Inspect the exact error or Skill before a materially corrected retry; close only a repeated non-progressing tactic, preserve completed work, and continue through another viable tactic within the selected implementation. Save user-facing outputs with save_artifacts."
	agentKernelReplDescription                         = "Execute bounded Python in the persistent stdlib-only, network-isolated control kernel. The independently injected Synon host bridge exposes only the current cell's allowed operations. Raw sockets and urllib downloads are unavailable here: inspect public pages with an advertised source tool and transfer complete public scientific files only with download_public_scientific_file. Use search_skills and skill for specialized guidance; Skill content is advisory, while each named host.mcp call remains governed by the live connector snapshot and exact method schema. For every distinct MCP method, inspect its session-scoped schema first. Make the first call in its own cell, save the raw returned value, and print only its type, top-level keys, and a short representation. Read nested fields only in a later cell from the exact observed shape; never guess a wrapper or item field. For multi-record work prefer host.mcp.search or host.mcp.collect and iterate its normalized records; do not iterate a direct mapping result as though it were a record list. For provider portability, a mapping result supports result[0] only when it contains exactly one unambiguous top-level record list; otherwise normal mapping semantics fail closed. host.mcp.list_servers() returns server-name strings and host.mcp.list_methods(server) returns method dictionaries; each method exposes both input_schema['properties'] and an iterable parameters list, so copy an exact advertised field and enum value instead of guessing query names or capitalization. Use manage_environments and manage_packages for third-party software, not this control kernel. A failed host operation ends that execution unit; inspect the actual contract, preserve completed evidence, and use a materially corrected operation. If the same tactic cannot progress, use another valid route to the same objective rather than stopping the task."
)

func (s *Server) resolveAgentKernelContext(ctx context.Context, sessionID string) *agentKernelContext {
	if s == nil || s.kernelManager == nil || ctx == nil {
		return nil
	}
	if evidence := s.kernelConfinementEvidence(false); !evidence.Available {
		return nil
	}
	access, workspaceDir, authorized := s.resolveAgentWorkspaceAuthority(ctx, sessionID)
	if !authorized {
		return nil
	}
	return &agentKernelContext{access: access, workspaceDir: workspaceDir}
}

// resolveAgentWorkspaceAuthority restores the durable frame-to-workspace
// identity shared by attachment validation and kernel execution. It grants no
// process or confinement authority; resolveAgentKernelContext deliberately adds
// those runtime checks before exposing this identity to executable tools.
func (s *Server) resolveAgentWorkspaceAuthority(
	ctx context.Context,
	sessionID string,
) (workspace.KernelFrameAccess, string, bool) {
	if s == nil || s.workspaceStore == nil || ctx == nil {
		return workspace.KernelFrameAccess{}, "", false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return workspace.KernelFrameAccess{}, "", false
	}

	frameID, workdir := "", ""
	var transcriptStream *transcriptstore.Stream
	if s.transcriptStore != nil {
		stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, sessionID)
		if err != nil {
			return workspace.KernelFrameAccess{}, "", false
		}
		if authoritative {
			if stream.Kind != transcriptstore.StreamKindFrameRef {
				return workspace.KernelFrameAccess{}, "", false
			}
			frameID = stream.FrameID
			transcriptStream = &stream
		}
	}
	if frameID == "" && s.sessionStore != nil {
		session, found, err := s.sessionStore.Get(sessionID)
		if err != nil {
			return workspace.KernelFrameAccess{}, "", false
		}
		if found {
			frameID = strings.TrimSpace(stringValue(session.Orchestration["frame_id"]))
			if frameID == "" {
				frameID = strings.TrimSpace(stringValue(session.Orchestration["frameId"]))
			}
			if frameID == "" {
				frameID = session.ID
			}
			workdir = strings.TrimSpace(session.WorkDir)
			if workdir == "" && session.Project != nil {
				workdir = strings.TrimSpace(session.Project.Path)
			}
		}
	}
	if frameID != "" && transcriptStream == nil && frameID != sessionID && s.transcriptStore != nil {
		stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, frameID)
		if err != nil {
			return workspace.KernelFrameAccess{}, "", false
		}
		if authoritative {
			if stream.Kind != transcriptstore.StreamKindFrameRef {
				return workspace.KernelFrameAccess{}, "", false
			}
			frameID = stream.FrameID
			transcriptStream = &stream
			workdir = ""
		}
	}

	if frameID == "" {
		return workspace.KernelFrameAccess{}, "", false
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, frameID)
	if err != nil || !found {
		return workspace.KernelFrameAccess{}, "", false
	}
	if transcriptStream != nil && (access.UserID != transcriptStream.OwnerID ||
		access.Frame.ProjectID != transcriptStream.ProjectID ||
		access.Frame.RootFrameID != transcriptStream.RootFrameID ||
		access.Frame.ID != transcriptStream.FrameID) {
		return workspace.KernelFrameAccess{}, "", false
	}
	if transcriptStream != nil {
		workdir = strings.TrimSpace(access.ProjectPath)
	}
	if workdir == "" || !filepath.IsAbs(workdir) {
		var err error
		workdir, err = s.defaultAgentKernelTaskWorkspace(
			access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID,
		)
		if err != nil {
			return workspace.KernelFrameAccess{}, "", false
		}
	}
	identity := &agentKernelContext{access: access, workspaceDir: workdir}
	canonicalWorkspace, err := s.canonicalAgentWorkspaceRoot(identity)
	if err != nil {
		return workspace.KernelFrameAccess{}, "", false
	}
	return access, canonicalWorkspace, true
}

func (s *Server) defaultAgentKernelWorkspace(projectID string) (string, error) {
	if s == nil || s.fileRoot == "" || projectID == "" {
		return "", errors.New("default kernel workspace identity is unavailable")
	}
	root, err := canonicalHostDirectory(s.fileRoot)
	if err != nil {
		return "", errors.New("default kernel workspace identity is unavailable")
	}
	workspaceID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-biomed-project-workspace:"+root+"\x00"+projectID))
	return filepath.Join(root+"-workspaces", workspaceID.String()), nil
}

func (s *Server) defaultAgentKernelTaskWorkspace(projectID, rootFrameID, rootIncarnationID string) (string, error) {
	projectWorkspace, err := s.defaultAgentKernelWorkspace(projectID)
	if err != nil {
		return "", err
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	rootIncarnationID = strings.TrimSpace(rootIncarnationID)
	if rootFrameID == "" || rootIncarnationID == "" {
		return "", errors.New("default task workspace identity is unavailable")
	}
	taskID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(
		"synon-biomed-task-workspace:"+projectWorkspace+"\x00"+rootFrameID+"\x00"+rootIncarnationID,
	))
	return filepath.Join(projectWorkspace, "tasks", taskID.String()), nil
}
