#!/usr/bin/env python3
"""Audit the migrated Web source, lockfile, provenance, and release boundary."""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from typing import Any


DEFAULT_ROOT = Path(__file__).resolve().parents[2]
FRONTEND = "frontend"
SOURCE_MANIFEST = "SOURCE_IMPORT_MANIFEST.json"
MIGRATION_MANIFEST = "MIGRATION_MANIFEST.json"
LICENSE_REPORT = "THIRD_PARTY_LICENSES.json"

BLOCKED_SERVER_PACKAGES = {
    "@synon-ai/web-host",
    "bcryptjs",
    "better-sqlite3",
    "cookie-parser",
    "cors",
    "express",
    "express-rate-limit",
    "jsonwebtoken",
    "multer",
    "sharp",
    "ws",
    "yauzl",
}

ALLOWED_ADAPTATIONS = {
    "packages/desktop/src/renderer/pages/conversation/GroupedHistory/ProjectHistorySection.tsx": "Synchronize the committed project list before visible reorder handles can receive input.",
    "tests/unit/renderer/ProjectHistorySection.dom.test.tsx": "Exercise immediate keyboard input on committed project handles before passive effects flush.",
    'packages/desktop/src/renderer/main.tsx': 'Remove the global runtime monkey-patch import so React, ResizeObserver, requestAnimationFrame, warnings, and errors retain their native observable behavior.',
    "packages/desktop/src/common/chat/chatLib.ts": "Carry validated canonical Transcript message coordinates and cancelled tool-call status through the migrated message model.",
    "packages/desktop/src/common/chat/normalizeToolCall.ts": "Normalize the canonical cancelled tool-call status without guessing terminal state.",
    "package.json": "Replace Bun/WebHost workspace runtime with a browser-only npm build package and exact dependency boundary.",
    "packages/desktop/package.json": "Remove @synon-ai/web-host workspace dependency and identify the browser-only renderer.",
    "packages/desktop/src/common/adapter/browser.ts": "Use the authenticated same-origin Go event WebSocket and adapt its event and control envelopes without retaining the legacy WebHost transport.",
    "packages/desktop/src/common/adapter/httpBridge.ts": "Retire the legacy WebSocket owner and bind the explicit kind-and-type bridge to the single authenticated realtime runtime.",
    "packages/desktop/src/common/adapter/ipcBridge.ts": "Represent the Go MCP OAuth pending-browser contract without changing the shared bridge API shape.",
    "packages/desktop/src/common/config/storage.ts": "Keep the browser runtime status model aligned with canonical completed, failed, and cancelled Transcript terminal states.",
    "packages/desktop/src/common/config/i18n-config.json": "Narrow the product language contract to fully maintained Simplified Chinese and English bundles.",
    "packages/desktop/src/renderer/hooks/context/AuthContext.tsx": "Replace the WebHost CSRF stubs with the Go session and double-submit CSRF bootstrap contract.",
    "packages/desktop/src/renderer/hooks/mcp/useMcpOAuth.ts": "Complete MCP OAuth in WebUI through a bounded popup-and-status-poll flow backed by the Go server.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/MarkdownViewer.tsx": "Use the locked Streamdown 1.5.1 contract and avoid its redundant runtime KaTeX CSS import because styles are bundled statically.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer.tsx": "Unify PDB, mmCIF, SDF, and related molecular formats on one Molstar preview with composition-aware controls, collapsed auxiliary panels, and source-fenced 2D interaction refresh.",
    "packages/desktop/src/renderer/index.html": "Project the root Synon Biomed product identity and load the CSP-compatible pre-render theme bootstrap without creating a second version authority.",
    "packages/desktop/src/renderer/pages/conversation/platforms/acp/AcpE2EStreamInjector.tsx": "Expose a session-gated E2E assistant-message injector so browser preview tests do not require a paid live model.",
    "packages/desktop/src/renderer/pages/onboarding/OnboardingFlow.tsx": "Keep bundled Python and R required while preserving optional first-run scientific-runtime choices, informational storage estimates, authenticated drafts, capability allowlists, and one recoverable launch.",
    "packages/desktop/src/renderer/pages/onboarding/OnboardingElicitCard.tsx": "Give the custom onboarding task input an explicit accessible label while preserving the task-selection contract.",
    "packages/desktop/src/renderer/pages/onboarding/onboardingModel.ts": "Derive initial connector and Skill state from the server allowlist without duplicating the immutable selection model.",
    "packages/desktop/src/renderer/pages/onboarding/onboarding.module.css": "Keep the three capability tabs and long scientific-runtime list readable and vertically scrollable without horizontal overflow on short or narrow viewports.",
    "packages/desktop/src/renderer/pages/settings/ComputeSettings.tsx": "Tighten settings copy for the Go-owned compute and inference control surface.",
    "packages/desktop/src/renderer/pages/settings/CredentialsSettings.tsx": "Tighten settings copy while preserving encrypted credential boundaries.",
    "packages/desktop/src/renderer/pages/settings/GeneralSettings.tsx": "Remove duplicated model controls and simplify local Go deployment guidance.",
    "packages/desktop/src/renderer/pages/settings/GovernanceSettings.tsx": "Condense governance copy for the migrated memory surface.",
    "packages/desktop/src/renderer/pages/settings/NetworkSettings.tsx": "Clarify the Go runtime network allowlist controls.",
    "packages/desktop/src/renderer/pages/settings/PermissionsSettings.tsx": "Clarify remembered permission review and revocation controls.",
    "packages/desktop/src/renderer/pages/settings/StorageSettings.tsx": "Compose independently loaded storage metadata, truthful usage, file rules, and cloud connections with safe mutation dialogs.",
    "packages/desktop/src/renderer/pages/settings/SynonBiomedExpertsSettings/ExpertWorkbench.tsx": "Improve responsive expert rows and compact long descriptions without dropping source detail.",
    "packages/desktop/src/renderer/pages/settings/SynonBiomedModelsSettings.tsx": "Condense model-provider settings copy for the migrated Web surface.",
    "packages/desktop/src/renderer/pages/settings/SynonBiomedSkillsSettings.tsx": "Unify installed Skill sources in one searchable library and consolidate acquisition into one action while preserving personal, import, toggle, and market workflows.",
    "packages/desktop/src/renderer/pages/settings/ToolsSettings/SynonBiomedMcpSettings.tsx": "Improve MCP connector density, localization, and responsive control layout.",
    "packages/desktop/src/renderer/pages/settings/ToolsSettings/index.tsx": "Clarify the expert MCP tool-management surface.",
    "packages/desktop/src/renderer/pages/guid/utils/assistantDefaults.ts": "Preserve owner-scoped connected MCP authority when an assistant uses automatic defaults and no explicit remembered selection exists.",
    "packages/desktop/src/renderer/pages/settings/components/SettingsPageHeader.tsx": "Give settings actions a stable responsive layout hook.",
    "packages/desktop/src/renderer/pages/settings/components/SettingsPageWrapper.tsx": "Provide route-aware responsive settings layout without nested page cards.",
    "packages/desktop/src/renderer/pages/settings/components/SettingsSider.tsx": "Align settings navigation icon states with the shared visual tokens.",
    "packages/desktop/src/renderer/pages/settings/components/SynonBiomedMemoryManager.tsx": "Render a recoverable inline memory-load failure state with an explicit retry action.",
    "packages/desktop/src/renderer/pages/settings/components/settings.css": "Stabilize responsive settings dimensions and shared visual tokens across desktop and narrow layouts.",
    "packages/desktop/src/renderer/services/SpeechToTextService.ts": "Attach the Go double-submit CSRF token to same-origin batch speech uploads without leaking it cross-origin.",
    "packages/desktop/src/renderer/services/authSession.ts": "Monitor session expiry and decorate unsafe same-origin browser requests with the Go CSRF token.",
    "packages/desktop/src/renderer/services/i18n/i18n-keys.d.ts": "Regenerate typed locale keys for required Python/R core runtimes and the optional first-run scientific-runtime catalog.",
    "packages/desktop/src/renderer/services/i18n/locales/en-US/guid.json": "Describe required Python/R core runtimes, optional first-run scientific runtimes, and informational storage estimates in the complete English onboarding locale.",
    "packages/desktop/src/renderer/services/i18n/locales/en-US/preview.json": "Add English labels for unified structure composition, collapsed 2D panels, and format-neutral interaction controls.",
    "packages/desktop/src/renderer/services/i18n/locales/zh-CN/guid.json": "Describe required Python/R core runtimes, optional first-run scientific runtimes, and informational storage estimates in the complete Simplified Chinese onboarding locale.",
    "packages/desktop/src/renderer/services/i18n/locales/zh-CN/preview.json": "Add Simplified Chinese labels for unified structure composition, collapsed 2D panels, and format-neutral interaction controls.",
    "packages/desktop/src/renderer/services/i18n/locales/zh-CN/settings.json": "Explain required scientific runtimes, unified user-managed storage, and Synon Go service guidance.",
    "packages/desktop/src/renderer/services/i18n/locales/zh-TW/settings.json": "Replace obsolete bun start deployment guidance with Synon Go service guidance.",
    "packages/desktop/src/renderer/services/onboardingService.ts": "Validate and persist the server-owned scientific-runtime selection without imposing client storage ceilings, while preserving exact Artifact and launch authority.",
    "packages/desktop/src/renderer/services/synonBiomedArtifactPreview.ts": "Route supported protein, complex, and ligand formats through the single composition-aware molecular preview authority.",
    "packages/desktop/src/renderer/styles/workspace-theme.css": "Provide a visible keyboard focus indicator for shared buttons across the migrated Web surface.",
    "packages/desktop/src/renderer/styles/themes/base.css": "Allow the authenticated onboarding and workbench layout to fit the supported 320px viewport without horizontal clipping.",
    "playwright.webui.config.ts": "Point browser acceptance at the future Go same-origin host and standardize disposable output.",
    "public/manifest.webmanifest": "Project the root Synon Biomed identity into installable PWA metadata and retain the text-free molecular icon set.",
    "tests/integration/i18n-packaged.test.ts": "Reference the browser-only production build command used by Synon Go.",
    "tests/integration/synonbiomedTestAuth.ts": "Provide an origin-scoped session and CSRF fetch client for authenticated Go integration tests.",
    "tests/unit/common-adapter/browserRealtimeError.test.ts": "Verify the browser adapter uses the authenticated Go event WebSocket and preserves runtime event and control envelopes.",
    "tests/unit/renderer/ArtifactPreview.dom.test.tsx": "Isolate persisted client configuration while testing native artifact preview and cloud export behavior.",
    "tests/unit/previews/SynonBiomedStructureViewer.dom.test.tsx": "Verify format-neutral composition controls, collapsed 2D panels, and source-invalidated interactions after minimization.",
    "tests/unit/previews/synonBiomedScientificPreview.test.ts": "Verify supported molecular extensions resolve to the unified structure viewer without a competing preview path.",
    "tests/unit/renderer/authSession.dom.test.tsx": "Verify authentication refresh and same-origin unsafe-request CSRF decoration in a real DOM fetch boundary.",
    "tests/unit/renderer/OnboardingFlow.dom.test.tsx": "Verify runtime selection, storage estimates, search, unavailable-host choices, and capability-tab keyboard behavior in onboarding.",
    "tests/unit/renderer/onboardingService.test.ts": "Verify scientific-runtime snapshot validation and exact selected-ID persistence alongside existing onboarding mutations.",
    "tests/unit/renderer/speechToTextService.test.ts": "Verify bounded speech uploads and same-origin-only CSRF header behavior.",
    "tests/unit/settings/GeneralSettings.dom.test.tsx": "Verify the streamlined general settings information architecture.",
    "tests/unit/settings/SynonBiomedMcpSettings.dom.test.tsx": "Verify localized and compact MCP connector presentation.",
    "tests/unit/settings/SynonBiomedMemoryManager.dom.test.tsx": "Verify the explicit memory-load failure and retry state.",
    "tests/unit/settings/SynonBiomedSkillsSettings.dom.test.tsx": "Verify localized Skill source labels and compact rows.",
    "tests/unit/settings/WorkspaceSafetySettings.dom.test.tsx": "Keep safety-settings assertions aligned with the clarified permission copy.",
    "tests/vitest.dom.setup.ts": "Provide the DOMMatrix browser API required by scientific viewers under jsdom.",
    "tests/web-e2e/synonbiomedArtifactMessageLinks.e2e.ts": "Create real disposable artifacts and verify assistant Markdown links open native previews at desktop and narrow sizes.",
    "tests/web-e2e/synonbiomedLoginBootstrap.e2e.ts": "Use configurable credentials and verify authenticated cookies, CSRF writes, runtime WebSocket bootstrap, and responsive onboarding interaction.",
    "tests/web-e2e/synonbiomedMcpSettings.e2e.ts": "Use configured credentials and verify the Go MCP settings flow in responsive Chromium.",
    "tests/web-e2e/synonbiomedRuntimeOperations.e2e.ts": "Use disposable authenticated runtime state for responsive control-surface acceptance.",
    "tests/web-e2e/synonbiomedScientificPreview.e2e.ts": "Upload real scientific fixtures and verify parsed, nonblank desktop and narrow previews.",
    "tests/web-e2e/synonbiomedSequenceNotebookPreview.e2e.ts": "Upload real sequence and notebook fixtures and verify their native responsive viewers.",
    "tests/web-e2e/synonbiomedSkillsSettings.e2e.ts": "Use configured credentials and verify the Go Skill lifecycle in responsive Chromium.",
    "uno.config.ts": "Use granular UnoCSS packages to reduce build dependency weight.",
    "vite.config.ts": "Use the granular UnoCSS Vite integration.",
    "vitest.config.ts": "Remove the excluded Bun WebHost project and isolate build-output verification from source tests.",
}

ALLOWED_ADDITIONS = {
    "packages/desktop/src/renderer/styles/tokens.css": "Own the single canonical visual-primitive scale (space, radius, elevation, type, weight, stacking) that every renderer module references instead of restating literals.",
    "packages/desktop/src/renderer/pages/settings/skills/SkillLibraryCard.tsx": "Render installed and draft skills with complete readable copy, declared-category icons and isolated keyboard actions.",
    "packages/desktop/src/renderer/pages/settings/skills/SkillFilePreview.tsx": "Render original Markdown and source files through distinct safe read-only previews.",
    "packages/desktop/src/renderer/pages/settings/skills/skillSourceLabel.ts": "Share truthful localized source labels between library cards and detail views without claiming authorship.",
    "packages/desktop/src/renderer/pages/settings/skills/SkillLibraryToolbar.tsx": "Provide one responsive search and research-field toolbar with accessible source/status refinement controls.",
    "packages/desktop/src/renderer/pages/settings/skills/SkillMarketModal.tsx": "Reuse the existing pinned-import market in a bounded dialog without resetting the installed library search or filters.",
    "packages/desktop/src/renderer/pages/settings/AppearanceSettings/presets/warm.css": "Keep the warm palette under its visual family name while retaining the original import record.",
    "packages/desktop/src/renderer/pages/settings/AppearanceSettings/presets/cool.css": "Keep the cool palette under its visual family name while retaining the original import record.",
    "packages/desktop/src/renderer/styles/workspace-theme.css": "Provide the shared visual shell under a product-neutral filename and CSS custom-property namespace.",
    "packages/desktop/src/common/chat/textPublicationCoverage.ts": "Bind Synon text replay suppression to conversation, segment, and durable publication coverage without discarding unseen publications.",
    "tests/integration/synonbiomedControlledLlmFixture.ts": "Provide the renamed controlled-provider integration fixture with explicit simulated-model provenance and real transcript transport.",
    "package-lock.json": "Deterministic npm build dependency lock.",
    "packages/desktop/src/common/adapter/ipcBridgeTypes.ts": "Define shared typed IPC facade contracts without creating a second transport authority.",
    "packages/desktop/src/common/adapter/ipcClientSettings.ts": "Centralize settings and model-client IPC construction behind the stable facade.",
    "packages/desktop/src/common/adapter/ipcConversationClient.ts": "Own conversation, transcript, realtime, and durable event IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcCoreClients.ts": "Compose small core IPC clients without retaining transport business logic in the facade.",
    "packages/desktop/src/common/adapter/ipcDatabaseClient.ts": "Own database inspection IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcFileClient.ts": "Own file and workspace IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcOperationsClient.ts": "Own operational and lifecycle IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcPreviewClient.ts": "Own preview and artifact-view IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcProviderClient.ts": "Own provider, credential, and compute IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/ipcRuntimeClient.ts": "Own runtime control and execution IPC bindings as one domain client.",
    "packages/desktop/src/common/adapter/conversationRuntimeProtocol.ts": "Validate canonical Go conversation runtime and terminal envelopes before they enter renderer state.",
    "packages/desktop/src/common/adapter/messageStreamProtocol.ts": "Validate canonical streamed message envelopes without guessing terminal or artifact state.",
    "packages/desktop/src/common/adapter/realtimeClient.ts": "Own one identity-bound authenticated realtime connection with fenced socket, listener, cursor, and timer resources.",
    "packages/desktop/src/common/adapter/realtimeProtocol.ts": "Validate the closed realtime control, direct, and durable event wire contract.",
    "packages/desktop/src/common/adapter/realtimeRenderer.ts": "Construct the renderer realtime runtime from the authenticated same-origin endpoint without import-time connection authority.",
    "packages/desktop/src/common/adapter/realtimeRuntime.ts": "Route validated durable and direct events through identity-epoch subscriptions and a stable external-store snapshot.",
    "packages/desktop/src/common/utils/errorRedaction.ts": "Map untrusted frontend failures to bounded diagnostic codes without leaking payloads or owner identity.",
    "packages/desktop/src/renderer/services/onboardingDraft.ts": "Persist bounded owner-scoped capability and scientific-runtime choices across authentication and refresh without retaining uploaded file bytes.",
    "packages/desktop/src/renderer/services/onboardingSuggestions.ts": "Run the authoritative bounded onboarding suggestion and AskUser flow without treating user profile content as instructions.",
    "packages/desktop/src/renderer/components/common/AccessibleActionDialog.tsx": "Provide a keyboard and focus-safe confirmation dialog for migrated destructive actions.",
    "packages/desktop/src/renderer/components/common/AccessibleContentDialog.tsx": "Provide a keyboard and focus-safe bounded content dialog for migrated scientific and runtime detail.",
    "packages/desktop/src/renderer/components/synonBiomed/runtime/SynonBiomedExecutionLogContent.tsx": "Render bounded structured execution logs without exposing raw runtime failures.",
    "packages/desktop/src/renderer/components/synonBiomed/runtime/longTaskStatusModel.ts": "Normalize long-task lifecycle and recovery state outside React components.",
    "packages/desktop/src/renderer/hooks/context/RealtimeContext.tsx": "Bind authoritative authentication identity to the single renderer realtime runtime and stable status store.",
    "packages/desktop/src/renderer/pages/artifact/AudioPreview.tsx": "Render bounded owner-scoped audio Artifact versions with native browser controls and explicit failure states.",
    "packages/desktop/src/renderer/pages/artifact/VideoPreview.tsx": "Render bounded owner-scoped video Artifact versions with native browser controls and explicit failure states.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppArtifactViewer.tsx": "Resolve exact Artifact-version inputs for the isolated read-only MCP App viewer.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppViewer.css": "Style the isolated MCP App host, loading state, and explicit read-only notice.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppViewer.tsx": "Host verified MCP App resources in a capability-minimal origin-bound iframe.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/mcpAppHostProtocol.ts": "Validate the bounded source-, origin-, session-, and generation-bound MCP App host protocol.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/mcpAppSaveContracts.ts": "Define one MCP App save-and-pin contract so editable viewers cannot invent competing persistence paths.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/StructureObjectList.tsx": "Render one composition-derived protein and ligand list with shared visibility, color, select-all, and clear controls.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer.css": "Style the unified molecular composition panel and collapsed 2D controls without resizing or replacing the established preview canvas.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/molstarDockingLigandShape.ts": "Normalize docking ligand shape and alternate-conformer selection for the shared Molstar structure path.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/molstarElectrostaticTheme.ts": "Provide one Molstar electrostatic color authority reused by protein and ligand surface representations.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/molstarInteractionAnchors.ts": "Render chemically meaningful multi-atom interaction centers from Molstar feature topology without structure-specific coordinate overrides.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/molstarStructureEngine.ts": "Own format-neutral Molstar loading, structure composition, surface styling, ligand extraction, and object visibility operations.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/structureElectrostaticMap.ts": "Validate and decode bounded aligned protein and ligand APBS maps before they enter the unified molecular preview.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/structureComposition.ts": "Classify parsed molecular structures into protein and primary-ligand components while excluding crystallization additives and duplicate conformers.",
    "packages/desktop/src/renderer/assets/icons/python-kernel.svg": "Add the text-free Python kernel icon used by the single governed Compute runtime panel.",
    "packages/desktop/src/renderer/pages/project/projectArtifactLibraryModel.ts": "Retain the imported pure artifact-library model under a module stem that cannot collide with the ProjectArtifactLibrary component on case-insensitive filesystems.",
    "packages/desktop/src/renderer/pages/settings/components/settingsPresentation.ts": "Shared Unicode-safe compaction for dense settings descriptions.",
    "packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/scientificPreviewError.ts": "Shared localized and non-sensitive error mapping for scientific preview failures.",
    "packages/desktop/src/renderer/pages/conversation/Messages/components/artifactReferenceModel.ts": "Resolve exact canonical message artifact_refs without tool-name, filename, path, or latest-version guessing.",
    "packages/desktop/src/renderer/components/Markdown/markdownImageUrlPolicy.ts": "Allow only bounded inert raster data URLs from streamed model and tool image blocks while rejecting active SVG and malformed payloads.",
    "packages/desktop/src/renderer/pages/conversation/Messages/components/MessageScientificFiles.css": "Own the compact workspace final-file tray dimensions and state without cross-module tool CSS overrides.",
    "packages/desktop/src/renderer/services/csrf.ts": "Central same-origin double-submit CSRF cookie and request-header implementation for the Go Web runtime.",
    "packages/desktop/src/renderer/services/i18n/i18n-keys.d.ts": "Generated i18n key declarations from migrated locale sources.",
    "packages/desktop/src/renderer/services/synonBiomedFileImportError.ts": "Map cloud, local, and SSH file-import failures to bounded recoverable user-facing states.",
    "packages/desktop/src/renderer/services/synonBiomedConversationTitle.ts": "Create bounded scientific conversation titles without exposing raw task content or adding model-side title authority.",
    "packages/desktop/src/renderer/services/agents/synonBiomedExpertLocalization.ts": "Localize authoritative expert profiles without changing their runtime identities or capabilities.",
    "public/theme-init.js": "Restore the validated cached light or dark appearance before renderer startup without allowing inline scripts.",
    "public/genomes/ucsc/NOTICE.txt": "Record the UCSC chromosome-size attribution and redistribution notice shipped with offline genome references.",
    "public/genomes/ucsc/PROVENANCE.json": "Pin the source URLs, hashes, and retrieval provenance for packaged UCSC genome references.",
    "public/genomes/ucsc/hg19.chrom.sizes": "Package checksum-bound hg19 chromosome sizes for offline genome preview.",
    "public/genomes/ucsc/hg38.chrom.sizes": "Package checksum-bound hg38 chromosome sizes for offline genome preview.",
    "public/genomes/ucsc/mm10.chrom.sizes": "Package checksum-bound mm10 chromosome sizes for offline genome preview.",
    "public/genomes/ucsc/mm39.chrom.sizes": "Package checksum-bound mm39 chromosome sizes for offline genome preview.",
    "public/rdkit/rdkit-worker.js": "Confine lockfile-pinned RDKit code generation to the dedicated worker and its path-specific production CSP.",
    "tests/integration/synonbiomedRealConversationFixture.ts": "Create and remove disposable real conversations so integration tests do not depend on deployment-specific migrated IDs.",
    "tests/unit/common/i18nSourceAudit.test.ts": "AST-based guard against unresolved translation keys, raw user-facing literals, and unsafe error notifications.",
    "tests/unit/common-adapter/conversationRuntimeProtocol.test.ts": "Verify exact accepted and rejected canonical runtime and terminal event envelopes.",
    "tests/unit/common-adapter/messageStreamProtocol.test.ts": "Verify strict streamed message, terminal, and artifact reference decoding.",
    "tests/unit/common-adapter/realtimeClient.test.ts": "Verify identity, socket, timer, cursor, reconnect, cleanup, and durable delivery authority under reentry.",
    "tests/unit/common-adapter/realtimeProtocol.test.ts": "Verify the closed realtime wire envelope and size boundary.",
    "tests/unit/common-adapter/realtimeRenderer.test.ts": "Verify Web and host endpoint resolution without import-time connection side effects.",
    "tests/unit/common-adapter/realtimeRuntime.test.ts": "Verify identity epochs, composite subscriptions, listener isolation, and reconnected delivery.",
    "tests/unit/common/modelCapabilities.test.ts": "Verify provider-neutral model capability inference without provider-name hard coding.",
    "tests/unit/renderer/artifactReferenceModel.test.ts": "Verify exact artifact/version association and fail-closed malformed canonical references.",
    "tests/unit/build/viteStaticCopyPaths.test.ts": "Verify Windows static-copy inputs are normalized before entering the glob boundary used by the production build.",
    "tests/unit/i18nTestUtils.tsx": "Shared deterministic bilingual renderer for localized component tests.",
    "tests/unit/workspace/useWorkspaceEvents.dom.test.ts": "Verify long-running workspace event state remains bounded, ordered, and recoverable in the renderer.",
    "tests/unit/chat/MessageSkillSuggest.test.ts": "Verify malformed Skill suggestions remain non-actionable and expose a localized recovery state.",
    "tests/unit/chat/MessageToolCall.dom.test.tsx": "Verify accessible keyboard and status behavior for migrated tool-call messages.",
    "tests/unit/chat/SynonBiomedDelegateDockRecovery.dom.test.tsx": "Verify failed child Agent operations expose deterministic retry and recovery behavior.",
    "tests/unit/previews/ImageViewer.dom.test.tsx": "Verify localized image preview controls and failure behavior.",
    "tests/unit/previews/PDFViewer.dom.test.tsx": "Verify localized PDF preview controls and failure behavior.",
    "tests/unit/previews/PreviewI18nParity.test.ts": "Keep the primary Chinese and English preview resources structurally aligned.",
    "tests/unit/previews/molstarStructureEngine.test.ts": "Verify unified molecular parsing, composition classification, electrostatic surfaces, ligand extraction, and visibility behavior.",
    "tests/unit/previews/structureElectrostaticMap.test.ts": "Verify aligned dual-component APBS response validation, bounded decoding, and malformed-map rejection.",
    "tests/unit/previews/SynonBiomedMcpAppViewer.dom.test.tsx": "Verify MCP App iframe confinement, loading, errors, read-only state, and lifecycle cleanup.",
    "tests/unit/previews/genomeReferenceModel.test.ts": "Verify packaged genome reference identity, bounds, and deterministic chromosome lookup.",
    "tests/unit/previews/mcpAppHostProtocol.test.ts": "Verify bounded MCP App host protocol validation and exact input round trips.",
    "tests/unit/renderer/FilesOverlay.dom.test.tsx": "Verify localized file-overlay states and actions.",
    "tests/unit/renderer/markdownImageUrlPolicy.test.ts": "Verify streamed inline raster images remain bounded and active or malformed data URLs fail closed.",
    "tests/unit/renderer/FirstUseGate.dom.test.tsx": "Verify authenticated first-use recovery, retry, and preserved onboarding draft state.",
    "tests/unit/renderer/AccessibleContentDialog.dom.test.tsx": "Verify focus, keyboard dismissal, labels, and content bounds for the shared dialog.",
    "tests/unit/renderer/conversation/useConversationRuntimeView.dom.test.tsx": "Verify runtime hydration, blocking failures, retries, and canonical terminal state in the conversation view.",
    "tests/unit/renderer/realtimeContext.dom.test.tsx": "Verify authentication identity transitions construct and dispose exactly one realtime authority.",
    "tests/unit/renderer/onboardingDraft.dom.test.ts": "Verify bounded owner-scoped onboarding and scientific-runtime draft persistence, corruption handling, and identity isolation.",
    "tests/unit/renderer/onboardingSuggestions.dom.test.ts": "Verify authoritative onboarding suggestions, timeout, cancellation, and strict AskUser option validation.",
    "tests/unit/renderer/projectArtifactLibraryModel.dom.test.ts": "Retain the imported artifact-library model regression under a module stem distinct from the component test on case-insensitive filesystems.",
    "tests/web-e2e/synonbiomedStreamingParity.e2e.ts": "Verify localized thinking, live tool stdout, inline images, and exact final artifact cards in desktop and narrow real browser paths.",
    "tests/unit/renderer/SynonBiomedNotesModal.dom.test.tsx": "Verify localized notes lifecycle and failure states.",
    "tests/unit/renderer/SynonBiomedSessionOptionsMenu.dom.test.tsx": "Retain the imported session-options component regression under its exact component name without colliding with the service test on case-insensitive filesystems.",
    "tests/unit/renderer/SynonBiomedTextArtifactViewer.dom.test.tsx": "Verify localized text-artifact rendering and actions.",
    "tests/unit/renderer/hooks/useMcpOAuth.dom.test.ts": "Verify browser popup, polling, blocked-window, and timeout behavior for Go MCP OAuth.",
    "tests/unit/settings/LanguageSwitcher.dom.test.tsx": "Verify the complete offline language catalog and persisted language switching.",
    "tests/unit/settings/settingsPresentation.test.ts": "Verify Unicode-safe settings description compaction and prefix removal.",
    "tests/unit/settings/settingsI18nTestUtils.tsx": "Expose the shared localized test renderer from the settings test boundary.",
    "tests/unit/synonbiomed/SynonBiomedUserMessageActions.dom.test.tsx": "Verify localized edit, retry, and branch actions on user messages.",
    "tests/unit/synonbiomed/SynonBiomedBranchMenu.dom.test.tsx": "Verify branch loading, selection, unavailable state, and bounded failures.",
    "tests/unit/synonbiomed/conversationBranches.test.ts": "Verify closed branch capability and branch coordinate contracts.",
    "tests/unit/synonbiomed/expertProfileLocalization.test.ts": "Verify expert localization preserves authoritative profile fields and fallbacks.",
    "tests/unit/synonbiomed/longTaskStatusModel.test.ts": "Verify long-task lifecycle normalization and invalid-state rejection.",
    "tests/unit/synonbiomed/fileImportError.test.ts": "Verify bounded file-import error classification without leaking raw failures.",
    "tests/unit/synonbiomed/synonBiomedHttp.test.ts": "Verify the authenticated Go HTTP boundary, fixed errors, and request ownership.",
    "tests/unit/web-e2e/officialChromeContract.test.ts": "Verify official Google Chrome identity, loopback CDP, and disposable profile acceptance boundaries.",
    "tests/web-e2e/synonBiomedScientificFixture.ts": "Create, upload, address, and delete disposable scientific artifacts through authenticated Go APIs.",
    "tests/web-e2e/synonGoConversationLifecycle.e2e.ts": "Verify the Go-owned project, conversation, message, realtime, and reload path against the production browser host.",
    "tests/web-e2e/synonGoWebCredentials.ts": "Centralize canonical and legacy-compatible credentials for authenticated Go Web browser acceptance tests.",
    "tests/web-e2e/globalSetup.ts": "Prepare authenticated first-run preferences through real Go APIs before each browser acceptance run.",
    "tests/web-e2e/officialChromeContract.ts": "Define the strict official Google Chrome executable, CDP endpoint, and disposable-profile contract.",
    "tests/web-e2e/officialChromeTest.ts": "Bind Playwright acceptance to one verified official Google Chrome CDP session and isolated context.",
    "tests/web-e2e/rdkitProduction.e2e.ts": "Verify real RDKit worker rendering, pixels, MIME, cache, and path-specific CSP under the production Go host.",
    "tests/web-e2e/synonGoFrameFixture.ts": "Apply deterministic frame and delegation state through the compiled Go browser-test fixture.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-typed-desktop-linux.png": "Retain the reviewed desktop visual baseline for typed AskUser history and terminal state.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-typed-narrow-linux.png": "Retain the reviewed narrow visual baseline for typed AskUser history and terminal state.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-typed-desktop-official-windows-chrome.png": "Retain the reviewed official Windows Chrome desktop baseline for typed AskUser history and terminal state.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-typed-narrow-official-windows-chrome.png": "Retain the reviewed official Windows Chrome narrow baseline for typed AskUser history and terminal state.",
    "tests/web-e2e/synonbiomedOnboarding.e2e.ts": "Verify fresh authentication, accessible five-step onboarding, capability reachability, draft recovery, Artifact refs, and workspace entry.",
    "tests/web-e2e/synonBiomedMcpApp.e2e.ts": "Verify isolated MCP App loading, editable save-to-Artifact versioning, and refresh rehydration against the production Go host in Google Chrome.",
    "tests/web-e2e/synonBiomedMemory.e2e.ts": "Verify the owner-scoped Memory settings lifecycle against the production Go host in Google Chrome.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-molecules-desktop-linux.png": "Reviewed desktop visual baseline for the molecule-selection ask_user workflow.",
    "tests/web-e2e/synonbiomedAskUser.e2e.ts-snapshots/ask-user-molecules-narrow-linux.png": "Reviewed narrow visual baseline for the molecule-selection ask_user workflow.",
    "tests/web-e2e/synonbiomedConversationBranches.e2e.ts": "Verify branch containment, selection, refresh, and responsive behavior against the real Go browser host.",
    "tests/web-e2e/synonbiomedConversationRecovery.e2e.ts-snapshots/conversation-deleted-tombstone-desktop-linux.png": "Reviewed desktop visual baseline for deleted-conversation recovery.",
    "tests/web-e2e/synonbiomedConversationRecovery.e2e.ts-snapshots/conversation-deleted-tombstone-narrow-linux.png": "Reviewed narrow visual baseline for deleted-conversation recovery.",
    "tests/web-e2e/synonbiomedModelListRecovery.e2e.ts-snapshots/model-list-unavailable-desktop-linux.png": "Reviewed desktop visual baseline for model-catalog recovery.",
    "tests/web-e2e/synonbiomedModelListRecovery.e2e.ts-snapshots/model-list-unavailable-narrow-linux.png": "Reviewed narrow visual baseline for model-catalog recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-model-overloaded-desktop-linux.png": "Reviewed desktop task-center baseline for model-overload recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-model-overloaded-narrow-linux.png": "Reviewed narrow task-center baseline for model-overload recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-model-unavailable-desktop-linux.png": "Reviewed desktop task-center baseline for unavailable-model recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-model-unavailable-narrow-linux.png": "Reviewed narrow task-center baseline for unavailable-model recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-safety-refusal-desktop-linux.png": "Reviewed desktop task-center baseline for safety-refusal recovery.",
    "tests/web-e2e/synonbiomedRuntimeFailureRecovery.e2e.ts-snapshots/runtime-recovery-safety-refusal-narrow-linux.png": "Reviewed narrow task-center baseline for safety-refusal recovery.",
    "tests/web-e2e/synonbiomedThirdPartyLicenses.e2e.ts-snapshots/third-party-licenses-desktop-linux.png": "Reviewed desktop visual baseline for the third-party license inventory.",
    "tests/web-e2e/synonbiomedThirdPartyLicenses.e2e.ts-snapshots/third-party-licenses-narrow-linux.png": "Reviewed narrow visual baseline for the third-party license inventory.",
    "tests/web-e2e/synonbiomedTranscriptRebase.e2e.ts": "Verify canonical Transcript rebase and reload behavior without restoring a legacy message authority.",
}

ALLOWED_ADDITION_RULES = (
    (
        ("packages/desktop/src/renderer/components/", "packages/desktop/src/renderer/hooks/", "packages/desktop/src/renderer/pages/", "packages/desktop/src/renderer/services/", "packages/desktop/src/renderer/utils/"),
        "Add bounded typed renderer components, hooks, pages, services, and utilities required by the authenticated Go-owned product surface.",
    ),
    (
        ("tests/unit/", "tests/web-e2e/"),
        "Add focused regression and real-browser coverage for the migrated product behavior.",
    ),
    (
        ("public/branding/", "public/dev-sw-cleanup.js"),
        "Add provenance-bound Synon Biomed branding assets and bounded development service-worker cleanup.",
    ),
)

ALLOWED_REMOVALS = {
    "packages/desktop/src/renderer/components/synonBiomed/runtime/ContextUsageIndicator.tsx": "Retire the unused ACP-derived usage ring after the authenticated request-usage card became the single composer authority.",
    "packages/desktop/src/renderer/styles/codex-theme.css": "Rename the shared visual shell to workspace-theme.css while preserving its original import provenance.",
    "packages/desktop/src/renderer/pages/conversation/Messages/acp/MessageAcpToolCall.tsx": "Consolidate tool rendering into the receipt-bound ToolOperationDetail and tool timeline.",
    "packages/desktop/src/renderer/pages/conversation/Messages/components/MessagePlan.tsx": "Consolidate plan rendering into the shared SynonBiomedPlanTree.",
    "packages/desktop/src/renderer/pages/conversation/platforms/acp/AcpE2EStreamInjector.tsx": "Retire production DOM event injection; browser acceptance uses the real transcript-backed controlled-provider fixture.",
    "tests/integration/synonbiomedRealLlmFixture.ts": "Rename the controlled-provider fixture to synonbiomedControlledLlmFixture so simulated provider responses are not presented as real-model evidence.",
    "tests/unit/chat/MessageAcpToolCall.dom.test.tsx": "Move tool presentation regression coverage to the unified MessageToolGroupSummary and ToolOperationDetail consumers.",
    "packages/desktop/src/renderer/hooks/file/useAutoPreviewOfficeFiles.ts": "Retire the unused desktop Office watcher after canonical Artifact references and the unified preview panel became the user-visible file authority.",
    "packages/desktop/src/renderer/hooks/system/useAutoPreviewOfficeFilesEnabled.ts": "Remove the orphaned Office auto-preview setting hook with its retired desktop watcher.",
    "tests/unit/renderer/useAutoPreviewOfficeFiles.dom.test.ts": "Remove the desktop Office watcher regression with its retired implementation; canonical Artifact preview behavior retains focused component coverage.",
    "tests/unit/synonbiomed/synonBiomedFramePlan.test.ts": "Remove filename-based plan discovery assertions after the typed Frame plan API became the sole authority; the real Frame plan integration regression remains.",
    'packages/desktop/src/renderer/utils/ui/runtimePatches.ts': 'Remove the global console, warning, error, ResizeObserver, and requestAnimationFrame monkey patch after real component and browser paths proved the native runtime is stable without it.',
    'tests/unit/renderer/runtimePatches.dom.test.ts': 'Remove the regression that asserted the retired global runtime monkey patch instead of user-visible component behavior.',
	"packages/desktop/src/renderer/pages/conversation/Preview/components/editors/HTMLEditor.tsx": "Retire the legacy HTML editor after DocumentWysiwygEditor became the single version-aware document editing authority.",
	"packages/desktop/src/renderer/pages/conversation/Preview/components/editors/MarkdownEditor.tsx": "Retire the legacy Markdown source editor after Streamdown preview and DocumentWysiwygEditor became the maintained editing surfaces.",
	"packages/desktop/src/renderer/pages/conversation/Preview/components/renderers/SelectionToolbar.tsx": "Retire the legacy renderer selection toolbar after the canonical preview region-comment layer assumed selection actions.",
	"packages/desktop/src/renderer/hooks/chat/useAutoScroll.ts": "Remove the unused legacy content-scroller hook after conversation scrolling converged on the Virtuoso-owned controller.",
    "packages/desktop/src/renderer/pages/conversation/Messages/useAutoScroll.ts": "Remove the competing DOM, RAF, read-cursor, and virtual-list scroll authority after Virtuoso assumed sole geometry ownership.",
    "tests/unit/renderer/useAutoScroll.dom.test.tsx": "Replace the deleted multi-authority scroll regression suite with the focused single-controller contract and real Chrome flow.",
    "packages/desktop/src/renderer/components/synonBiomed/runtime/streamingMessagesModel.ts": "Remove the second streaming overlay authority after live stdout converged on the root-scoped conversation streaming projection.",
    "packages/desktop/src/renderer/components/synonBiomed/runtime/streamingModel.ts": "Remove the mixed text-thinking-stdout compatibility model after text and thinking remained solely owned by durable message.stream.",
    "packages/desktop/src/renderer/services/synonBiomedStreaming.ts": "Move streaming recovery I/O beside the unified conversation streaming model and store.",
    "tests/unit/synonbiomed/streamingMessagesModel.test.ts": "Replace the legacy overlay regression with the unified conversation streaming projection contract.",
    "tests/unit/synonbiomed/streamingModel.test.ts": "Replace the mixed compatibility-model regression with the consumed conversation streaming recovery slice contract.",
    "packages/desktop/src/renderer/pages/conversation/Messages/components/MessageToolGroup.tsx": "Retire the duplicate legacy tool-group renderer after every tool-group path converged on the workspace MessageToolGroupSummary projection.",
    "packages/desktop/src/renderer/components/synonBiomed/runtime/SynonBiomedKernelNotebook.tsx": "Retire the duplicate executable kernel notebook after the governed Compute runtime panel became the single kernel inventory, status, interrupt, clear, and task-level stop surface.",
    "packages/desktop/src/renderer/services/skillsCatalog.ts": "Relocate the Skill catalog client into the canonical skills service domain without changing behavior.",
    "packages/desktop/src/renderer/services/synonBiomedExpertProfiles.ts": "Relocate the expert profile client into the canonical agents service domain without changing behavior.",
    "packages/desktop/src/renderer/services/synonBiomedMcpApps.ts": "Relocate the MCP app client into the canonical MCP service domain without changing behavior.",
    "packages/desktop/src/renderer/services/synonBiomedSkillLibrary.ts": "Relocate the Skill library client into the canonical skills service domain without changing behavior.",
    "packages/desktop/src/renderer/assets/logos/brand/app.png": "Remove the obsolete Synon-AI wordmark after all product surfaces switched to the provenance-tracked text-free PWA icon and HTML Synon Biomed name.",
    "packages/desktop/src/renderer/hooks/chat/useInitialMessage.ts": "Remove an unused candidate hook superseded by the tested ACP initial-message implementation.",
    "packages/desktop/src/renderer/pages/project/projectArtifactLibrary.ts": "Rename the imported pure artifact-library model so extensionless imports cannot collide with the ProjectArtifactLibrary component on case-insensitive filesystems.",
    "packages/desktop/src/renderer/pages/conversation/Messages/components/toolArtifactModel.ts": "Remove filename and tool-name artifact guessing after canonical message artifact_refs became the only association authority.",
    "packages/desktop/src/renderer/pages/TestShowcase.tsx": "Remove an unreferenced development showcase containing corrupted legacy display text.",
    "tests/unit/renderer/projectArtifactLibrary.dom.test.ts": "Rename the imported artifact-library model regression so its module stem cannot collide with the component test on case-insensitive filesystems.",
    "tests/unit/renderer/SynonBiomedSessionOptions.dom.test.tsx": "Rename the imported session-options component regression to its exact component name so it cannot collide with the service test on case-insensitive filesystems.",
    "tests/unit/renderer/toolArtifactModel.test.ts": "Remove the superseded artifact-guessing regression together with its deleted heuristic implementation.",
    "tests/unit/synonbiomed/SynonBiomedKernelNotebook.dom.test.tsx": "Remove the obsolete executable-notebook regression with the duplicate kernel notebook component; the canonical Compute panel has its own interaction coverage.",
    "packages/desktop/src/renderer/pages/conversation/Workspace/utils/synonBiomedWorkspaceHydration.ts": "Remove the duplicate workspace hydration helper after the canonical workspace tree and frame-read services assumed sole authority.",
    "packages/desktop/src/renderer/pages/conversation/components/ChatHistory.tsx": "Remove the legacy non-virtualized chat history after MessageList became the single canonical conversation renderer.",
    "packages/desktop/src/renderer/pages/conversation/platforms/acp/SynonBiomedConversationSyncNotice.tsx": "Remove the duplicate sync notice after runtime status and terminal failure surfaces became authoritative.",
    "packages/desktop/src/renderer/pages/project/ProjectWorkbench.tsx": "Retire the duplicate project workbench page after project routes converged on the project and conversation workspace surfaces.",
    "packages/desktop/src/renderer/pages/project/projectWorkbenchModel.ts": "Retire the duplicate project-workbench view model with its removed page.",
    "packages/desktop/src/renderer/pages/settings/PermissionsSettings.tsx": "Retire the standalone settings page after permission controls moved into the unified settings information architecture.",
    "packages/desktop/src/renderer/pages/settings/PetSettings.tsx": "Retire the unsupported pet settings route from the focused Synon Biomed product surface.",
    "packages/desktop/src/renderer/pages/settings/SystemSettings.tsx": "Retire the standalone system settings page after supported controls moved into the unified general settings surface.",
    "packages/desktop/src/renderer/pages/settings/models/permissionPresentation.ts": "Remove the legacy permission presentation model with its retired standalone page.",
    "tests/unit/renderer/ProjectWorkbench.dom.test.tsx": "Remove the obsolete duplicate project workbench regression with the retired page.",
    "tests/unit/renderer/SynonBiomedConversationSyncNotice.dom.test.tsx": "Remove the obsolete sync-notice regression with the retired component.",
    "tests/unit/renderer/projectWorkbenchModel.test.ts": "Remove the duplicate project workbench model regression with its retired model.",
    "tests/unit/settings/SystemSettings.dom.test.tsx": "Remove the standalone system settings regression after route convergence.",
    "tests/unit/settings/backgroundUtils.test.ts": "Remove the legacy custom-background regression after theme authority moved to builtin themes.",
    "tests/unit/workspace/synonBiomedWorkspaceHydration.test.ts": "Remove the duplicate hydration regression with its retired helper.",
    "tests/web-e2e/synonbiomedProjectWorkbench.e2e.ts": "Remove the duplicate project workbench browser flow after route convergence; its orphan snapshots are quarantined outside the repository.",
}

ALLOWED_REMOVAL_RULES = (
    (
        ("packages/desktop/src/renderer/assets/icons/", "packages/desktop/src/renderer/assets/themes/"),
        "Retire unused legacy icon and theme assets after the branded PWA assets and builtin workspace theme system became authoritative.",
    ),
    (
        ("packages/desktop/src/renderer/pages/cron/", "tests/unit/cron/", "tests/unit/renderer/cron/"),
        "Retire the duplicate legacy scheduled-task Web pages and their isolated UI tests while the Go routine scheduler and governed task runtime remain authoritative.",
    ),
    (
        ("packages/desktop/src/renderer/pages/settings/AppearanceSettings/",),
        "Retire the legacy arbitrary CSS-theme editor and preset gallery after the bounded builtin theme authority replaced it.",
    ),
    (
        tuple(
            f"packages/desktop/src/renderer/services/i18n/locales/{locale}/"
            for locale in (
                "de-DE",
                "es-ES",
                "fa-IR",
                "ja-JP",
                "ko-KR",
                "pt-BR",
                "ru-RU",
                "tr-TR",
                "uk-UA",
                "zh-TW",
            )
        ),
        "Retire incomplete legacy locale bundles after the product language contract was narrowed to complete Simplified Chinese and English coverage.",
    ),
)

# Exact adaptations above retain narrow historical reasons. These ordered
# domain rules classify the later full-product migration. The fingerprint below
# locks the complete path and source/target hash set, so a prefix cannot silently
# authorize future edits or additions.
ALLOWED_ADAPTATION_RULES = (
    (
        (
            "packages/desktop/src/common/adapter/",
            "packages/desktop/src/common/config/",
            "packages/desktop/src/common/theme/",
            "packages/desktop/src/common/types/",
        ),
        "Align shared browser contracts, configuration, themes, and typed preview/provider models with the Go-owned Synon Biomed runtime.",
    ),
    (
        (
            "packages/desktop/src/renderer/styles/",
            "packages/desktop/src/renderer/theme/",
            "packages/desktop/src/renderer/types.d.ts",
            "public/pwa/",
            "public/sw.js",
        ),
        "Converge global styles, builtin theme declarations, PWA assets, service-worker behavior, and renderer type declarations on the focused Web product.",
    ),
    (
        ("packages/desktop/src/common/config/i18n.ts",),
        "Centralize native language labels while preserving normalization for the complete offline locale catalog.",
    ),
    (
        ("packages/desktop/src/common/utils/",),
        "Keep shared browser-safe utility behavior aligned with the complete migrated locale and runtime contracts.",
    ),
    (
        (
            "packages/desktop/src/renderer/components/Markdown/",
            "packages/desktop/src/renderer/components/base/",
        ),
        "Adapt shared renderer primitives for safe native previews, localized errors, accessibility, and responsive Web use.",
    ),
    (
        ("packages/desktop/src/renderer/components/chat/",),
        "Connect the composer, queue, attachments, and chat actions to the Go-owned conversation runtime with localized responsive states.",
    ),
    (
        ("packages/desktop/src/renderer/components/layout/",),
        "Adapt the desktop layout, command palette, navigation, and window controls to authenticated responsive browser operation.",
    ),
    (
        ("packages/desktop/src/renderer/components/media/",),
        "Harden media selection, upload, diff, preview, and embedded-content states for the Go Web trust boundary.",
    ),
    (
        ("packages/desktop/src/renderer/components/settings/",),
        "Adapt shared settings dialogs and language controls to the Go configuration and browser filesystem contracts.",
    ),
    (
        ("packages/desktop/src/renderer/components/synonBiomed/",),
        "Project authoritative Go files, notes, approvals, models, kernels, sessions, and runtime operations into the migrated workbench.",
    ),
    (
        ("packages/desktop/src/renderer/components/workspace/",),
        "Adapt reusable workspace folder selection to the authenticated Go filesystem and project-root boundary.",
    ),
    (
        (
            "packages/desktop/src/renderer/hooks/",
            "packages/desktop/src/renderer/main.tsx",
        ),
        "Bootstrap authenticated Web runtime state and adapt assistant and MCP hooks without retaining Electron server authority.",
    ),
    (
        ("packages/desktop/src/renderer/pages/artifact/",),
        "Connect immutable Go artifacts, annotations, refinement, and native viewers with bounded localized failure handling.",
    ),
    (
        ("packages/desktop/src/renderer/pages/conversation/Preview/",),
        "Migrate preview history and real scientific, office, document, and media viewers with safe parsing and responsive controls.",
    ),
    (
        ("packages/desktop/src/renderer/pages/conversation/",),
        "Connect conversation history, messages, workspace, delegation, commands, and realtime recovery to the Go runtime.",
    ),
    (
        ("packages/desktop/src/renderer/pages/cron/",),
        "Adapt scheduled-task creation, inspection, and lifecycle controls to the Go routine APIs.",
    ),
    (
        (
            "packages/desktop/src/renderer/pages/guid/",
            "packages/desktop/src/renderer/pages/login/",
            "packages/desktop/src/renderer/pages/project/",
        ),
        "Migrate authenticated login, onboarding, project workbench, and artifact workflows to the Go product shell.",
    ),
    (
        ("packages/desktop/src/renderer/pages/settings/",),
        "Expose Go-owned agents, skills, MCP, models, compute, memory, permissions, credentials, storage, and governance settings.",
    ),
    (
        (
            "packages/desktop/src/renderer/services/",
            "packages/desktop/src/renderer/utils/",
        ),
        "Implement typed Go API clients, complete localization resources, runtime projections, and browser-safe presentation models.",
    ),
    (
        ("scripts/generate-i18n-types.js",),
        "Generate deterministic i18n declarations with the locked local formatter and no Bun fallback.",
    ),
    (
        ("tests/integration/",),
        "Exercise migrated Go API, saved-model, authentication, and packaged-localization contracts through real protocol fixtures.",
    ),
    (
        ("tests/web-e2e/",),
        "Exercise authenticated Go runtime, project, conversation, delegation, settings, and preview workflows in real responsive Chromium.",
    ),
    (
        ("tests/unit/",),
        "Align behavioral tests with the migrated Go contracts, full localization, responsive states, and deterministic test isolation.",
    ),
)

APPROVED_ADAPTATION_FINGERPRINT = "97d37ddc4daffd85fdf10b84e751848f67a337118e8fec8ae1c374e92ce5b339"
APPROVED_REMOVAL_FINGERPRINT = "70a676732efea3030080147fc7100096248f3fc0ce13b80256726d97fef98b5c"
APPROVED_ADDITION_FINGERPRINT = "91560d67f941cd4c8db188401c1f54d6ea106321d38877981d012c018a14b02b"

MANUAL_LICENSES = {
    "@nightingale-elements/nightingale-msa": {
        "license": "MIT",
        "basis": "upstream repository license",
        "url": "https://github.com/ebi-webcomponents/nightingale/blob/main/LICENSE",
    },
    "emitter-component": {
        "license": "MIT",
        "basis": "transitive package metadata omission; upstream package license",
    },
    "format": {
        "license": "MIT",
        "basis": "transitive package metadata omission; upstream package license",
    },
    "khroma": {
        "license": "MIT",
        "basis": "transitive package metadata omission; upstream package license",
    },
}

BUILTIN_IMPORTS = {
    "child_process",
    "fs",
    "os",
    "path",
    "vitest",
}


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def iter_files_without(root: Path, ignored_directories: set[str]):
    """Yield files without descending into generated dependency/output trees."""
    for directory, names, files in os.walk(root, topdown=True, followlinks=False):
        names[:] = sorted(name for name in names if name not in ignored_directories)
        for name in sorted(files):
            yield Path(directory, name)


def json_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def read_json(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def adaptation_reason(relative: str) -> str | None:
    exact = ALLOWED_ADAPTATIONS.get(relative)
    if exact is not None:
        return exact
    for prefixes, reason in ALLOWED_ADAPTATION_RULES:
        if any(relative == prefix or (prefix.endswith("/") and relative.startswith(prefix)) for prefix in prefixes):
            return reason
    return None


def removal_reason(relative: str) -> str | None:
    exact = ALLOWED_REMOVALS.get(relative)
    if exact is not None:
        return exact
    for prefixes, reason in ALLOWED_REMOVAL_RULES:
        if any(relative.startswith(prefix) for prefix in prefixes):
            return reason
    return None


def addition_reason(relative: str) -> str | None:
    exact = ALLOWED_ADDITIONS.get(relative)
    if exact is not None:
        return exact
    for prefixes, reason in ALLOWED_ADDITION_RULES:
        if any(relative == prefix or (prefix.endswith("/") and relative.startswith(prefix)) for prefix in prefixes):
            return reason
    return None


def adaptation_fingerprint(adaptations: list[dict[str, Any]]) -> str:
    records = [
        {
            "path": record["path"],
            "sourceSHA256": record["sourceSHA256"],
            "targetSHA256": record["targetSHA256"],
        }
        for record in adaptations
    ]
    payload = json.dumps(records, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("ascii")
    return hashlib.sha256(payload).hexdigest()


def removal_fingerprint(removals: list[dict[str, Any]]) -> str:
    records = [
        {
            "path": record["path"],
            "sourceSHA256": record["sourceSHA256"],
        }
        for record in removals
    ]
    payload = json.dumps(records, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("ascii")
    return hashlib.sha256(payload).hexdigest()


def addition_fingerprint(additions: list[dict[str, Any]]) -> str:
    records = [
        {
            "path": record["path"],
            "sha256": record["sha256"],
        }
        for record in additions
    ]
    payload = json.dumps(records, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("ascii")
    return hashlib.sha256(payload).hexdigest()


def package_name_from_lock_path(path: str) -> str:
    marker = "node_modules/"
    if marker not in path:
        return path
    value = path.rsplit(marker, 1)[1]
    parts = value.split("/")
    if value.startswith("@") and len(parts) >= 2:
        return "/".join(parts[:2])
    return parts[0]


def license_report(frontend: Path, package: dict[str, Any], lock: dict[str, Any]) -> dict[str, Any]:
    direct_runtime = set(package.get("dependencies", {}))
    direct_development = set(package.get("devDependencies", {}))
    records: list[dict[str, Any]] = []
    workspace_links: list[dict[str, Any]] = []
    unresolved_direct: list[str] = []
    license_counts: collections.Counter[str] = collections.Counter()
    for lock_path, metadata in sorted(lock.get("packages", {}).items()):
        if not lock_path or lock_path.startswith("packages/"):
            continue
        name = package_name_from_lock_path(lock_path)
        if metadata.get("link"):
            target = metadata.get("resolved", "")
            target_path = (frontend / target).resolve()
            target_metadata = lock.get("packages", {}).get(target, {})
            if (
                not target.startswith("packages/")
                or not target_path.is_relative_to(frontend.resolve())
                or target_metadata.get("name") != name
            ):
                raise RuntimeError(f"invalid local workspace link: {lock_path}")
            local_package = read_json(target_path / "package.json")
            if any(local_package.get(key) != target_metadata.get(key) for key in ("name", "version")):
                raise RuntimeError(f"workspace package identity mismatch: {lock_path}")
            workspace_links.append({
                "name": name,
                "version": local_package.get("version"),
                "path": target,
                "basis": "local workspace package; source licensing reviewed separately",
            })
            continue
        license_name = metadata.get("license")
        basis = "package-lock metadata"
        url = None
        if not license_name and name in MANUAL_LICENSES:
            manual = MANUAL_LICENSES[name]
            license_name = manual["license"]
            basis = manual["basis"]
            url = manual.get("url")
        if not license_name:
            license_name = "NOASSERTION"
        direct_kind = None
        if name in direct_runtime:
            direct_kind = "runtime-build"
        elif name in direct_development:
            direct_kind = "development"
        if direct_kind and license_name == "NOASSERTION":
            unresolved_direct.append(name)
        license_counts[license_name] += 1
        record = {
            "name": name,
            "version": metadata.get("version"),
            "license": license_name,
            "directKind": direct_kind,
            "basis": basis,
        }
        if url:
            record["sourceUrl"] = url
        records.append(record)
    if unresolved_direct:
        raise RuntimeError("direct dependency licenses unresolved: " + ", ".join(sorted(set(unresolved_direct))))
    return {
        "schemaVersion": 1,
        "package": package.get("name"),
        "lockfileSHA256": sha256_file(frontend / "package-lock.json"),
        "summary": {
            "records": len(records),
            "workspaceLinkRecords": len(workspace_links),
            "directRuntimeBuildDependencies": len(direct_runtime),
            "directDevelopmentDependencies": len(direct_development),
            "licenseCounts": dict(sorted(license_counts.items())),
            "unresolvedTransitiveRecords": license_counts["NOASSERTION"],
            "unresolvedDirectRecords": 0,
        },
        "records": records,
        "workspaceLinks": workspace_links,
    }


def audit_package(frontend: Path, package: dict[str, Any], lock: dict[str, Any]) -> None:
    dependencies = set(package.get("dependencies", {}))
    blocked = sorted(dependencies & BLOCKED_SERVER_PACKAGES)
    if blocked:
        raise RuntimeError("server/runtime dependencies remain in frontend: " + ", ".join(blocked))
    scripts = package.get("scripts", {})
    forbidden_script = re.compile(r"(^|\s)(bun|bunx)(\s|$)|web-host|webui\.ts", re.IGNORECASE)
    invalid_scripts = [name for name, command in scripts.items() if forbidden_script.search(str(command))]
    if invalid_scripts:
        raise RuntimeError("Bun/WebHost runtime scripts remain: " + ", ".join(sorted(invalid_scripts)))
    root_lock = lock.get("packages", {}).get("", {})
    if root_lock.get("name") != package.get("name") or root_lock.get("version") != package.get("version"):
        raise RuntimeError("package-lock root identity does not match package.json")
    if root_lock.get("dependencies", {}) != package.get("dependencies", {}):
        raise RuntimeError("package-lock runtime dependencies do not match package.json")
    if root_lock.get("devDependencies", {}) != package.get("devDependencies", {}):
        raise RuntimeError("package-lock development dependencies do not match package.json")


def migration_manifest(frontend: Path, source: dict[str, Any]) -> dict[str, Any]:
    source_records = {record["path"]: record for record in source.get("files", [])}
    adaptations = []
    removals = []
    unchanged = 0
    missing = []
    for relative, record in sorted(source_records.items()):
        target = frontend / relative
        if not target.is_file():
            reason = removal_reason(relative)
            if reason is None:
                missing.append(relative)
            else:
                removals.append(
                    {
                        "path": relative,
                        "sourceSHA256": record["sha256"],
                        "sourceBytes": record["bytes"],
                        "reason": reason,
                    }
                )
            continue
        target_hash = sha256_file(target)
        if target_hash == record["sha256"]:
            unchanged += 1
            continue
        reason = adaptation_reason(relative)
        if reason is None:
            raise RuntimeError(f"unclassified migrated-source change: {relative}")
        adaptations.append(
            {
                "path": relative,
                "sourceSHA256": record["sha256"],
                "targetSHA256": target_hash,
                "bytes": target.stat().st_size,
                "reason": reason,
            }
        )
    if missing:
        raise RuntimeError("migrated source files are missing: " + ", ".join(missing[:20]))

    unknown_removals = sorted(set(ALLOWED_REMOVALS) - set(source_records))
    if unknown_removals:
        raise RuntimeError("removal policy references unknown source files: " + ", ".join(unknown_removals))
    retained_removals = sorted(relative for relative in ALLOWED_REMOVALS if (frontend / relative).exists())
    if retained_removals:
        raise RuntimeError("removal policy files unexpectedly remain: " + ", ".join(retained_removals))

    removal_digest = removal_fingerprint(removals)
    if removal_digest != APPROVED_REMOVAL_FINGERPRINT:
        raise RuntimeError(
            "migrated-source removal fingerprint mismatch: "
            f"expected {APPROVED_REMOVAL_FINGERPRINT}, got {removal_digest}"
        )

    fingerprint = adaptation_fingerprint(adaptations)
    if fingerprint != APPROVED_ADAPTATION_FINGERPRINT:
        raise RuntimeError(
            "migrated-source adaptation fingerprint mismatch: "
            f"expected {APPROVED_ADAPTATION_FINGERPRINT}, got {fingerprint}"
        )

    missing_required_additions = sorted(relative for relative in ALLOWED_ADDITIONS if not (frontend / relative).is_file())
    if missing_required_additions:
        raise RuntimeError("required migrated frontend additions are missing: " + ", ".join(missing_required_additions[:20]))

    additions = []
    unexpected_additions = []
    for path in iter_files_without(frontend, {"node_modules"}):
        relative_path = path.relative_to(frontend)
        relative = relative_path.as_posix()
        if relative in source_records or relative in {SOURCE_MANIFEST, MIGRATION_MANIFEST, LICENSE_REPORT}:
            continue
        reason = addition_reason(relative)
        if reason is None:
            unexpected_additions.append(relative)
            continue
        additions.append(
            {
                "path": relative,
                "sha256": sha256_file(path),
                "bytes": path.stat().st_size,
                "reason": reason,
            }
        )
    if unexpected_additions:
        raise RuntimeError(
            "unclassified migrated frontend additions: " + ", ".join(sorted(unexpected_additions)[:30])
        )
    additions.sort(key=lambda record: record["path"])
    additions_digest = addition_fingerprint(additions)
    if additions_digest != APPROVED_ADDITION_FINGERPRINT:
        raise RuntimeError(
            "migrated-source addition fingerprint mismatch: "
            f"expected {APPROVED_ADDITION_FINGERPRINT}, got {additions_digest}"
        )
    return {
        "schemaVersion": 1,
        "source": source.get("source"),
        "sourceMutationPolicy": "read-only",
        "sourceImportManifestSHA256": sha256_file(frontend / SOURCE_MANIFEST),
        "adaptationFingerprintSHA256": fingerprint,
        "removalFingerprintSHA256": removal_digest,
        "additionFingerprintSHA256": additions_digest,
        "summary": {
            "sourceFiles": len(source_records),
            "unchangedSourceFiles": unchanged,
            "adaptedSourceFiles": len(adaptations),
            "removedSourceFiles": len(removals),
            "addedFiles": len(additions),
        },
        "adaptations": adaptations,
        "removals": removals,
        "additions": additions,
    }


def audit_tree(frontend: Path, require_clean: bool) -> None:
    blocked_names = {"coverage", "dist", "out", "test-results", "__pycache__"}
    failures = []
    if require_clean and (frontend / "node_modules").exists():
        failures.append("node_modules")
    for directory, names, files in os.walk(frontend, topdown=True, followlinks=False):
        current = Path(directory)
        retained = []
        for name in sorted(names):
            candidate = current / name
            relative = candidate.relative_to(frontend)
            if name == "node_modules":
                continue
            if name in blocked_names:
                failures.append(relative.as_posix())
                continue
            retained.append(name)
        names[:] = retained
        for name in sorted(files):
            path = current / name
            relative = path.relative_to(frontend)
            if path.stat().st_size > 10 * 1024 * 1024:
                failures.append(relative.as_posix() + " (>10 MiB)")
    if failures:
        raise RuntimeError("blocked frontend build/residue paths: " + ", ".join(sorted(set(failures))[:30]))


def audit_module_stem_collisions(frontend: Path) -> None:
    """Reject extensionless module names that collide on case-insensitive filesystems."""

    module_suffixes = {".cjs", ".cts", ".js", ".jsx", ".mjs", ".mts", ".ts", ".tsx"}
    ignored_parts = {"__pycache__", "coverage", "dist", "node_modules", "out", "test-results"}
    paths_by_stem: dict[str, list[str]] = {}
    for path in iter_files_without(frontend, ignored_parts):
        if path.suffix.lower() not in module_suffixes:
            continue
        relative = path.relative_to(frontend)
        extensionless = (relative.parent / path.stem).as_posix()
        paths_by_stem.setdefault(extensionless.casefold(), []).append(relative.as_posix())

    collisions = [sorted(paths) for paths in paths_by_stem.values() if len(paths) > 1]
    if collisions:
        details = "; ".join(" <> ".join(paths) for paths in sorted(collisions))
        raise RuntimeError("case-insensitive extensionless module collision: " + details)


def write_or_check(path: Path, content: bytes, check: bool) -> None:
    if check:
        if not path.is_file() or path.read_bytes() != content:
            raise RuntimeError(f"generated frontend audit artifact is stale: {path.name}")
        return
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_bytes(content)
    os.replace(temporary, path)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--require-clean", action="store_true")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    frontend = args.root.resolve() / FRONTEND
    package = read_json(frontend / "package.json")
    lock = read_json(frontend / "package-lock.json")
    source = read_json(frontend / SOURCE_MANIFEST)
    audit_package(frontend, package, lock)
    audit_tree(frontend, args.require_clean)
    audit_module_stem_collisions(frontend)
    licenses = license_report(frontend, package, lock)
    migration = migration_manifest(frontend, source)
    write_or_check(frontend / LICENSE_REPORT, json_bytes(licenses), args.check)
    write_or_check(frontend / MIGRATION_MANIFEST, json_bytes(migration), args.check)
    mode = "checked" if args.check else "generated"
    print(
        f"frontend-migration-audit: {mode}; "
        f"{migration['summary']['sourceFiles']} source files, "
        f"{migration['summary']['adaptedSourceFiles']} adaptations, "
        f"{licenses['summary']['records']} dependency license records"
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (FileNotFoundError, RuntimeError, ValueError, json.JSONDecodeError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        raise SystemExit(1)
