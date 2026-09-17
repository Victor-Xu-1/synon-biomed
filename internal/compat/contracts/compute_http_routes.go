package contracts

const computeRouteSource = "runtime/server/synonbiomed.bundle.js"
const computeRouteSourceLine = 20349

// HTTPRoutes is the public compute route surface recovered from the v1.1
// Fastify registration block. It is separate from the 190 client service
// wrappers so both original denominators remain auditable.
var HTTPRoutes = []HTTPRoute{
	{Method: "GET", Path: "/api/compute/gpu", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/gpu/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/gpu/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/bionemo/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/bionemo/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/gpu/detect", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/providers", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/providers/:name", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PATCH", Path: "/api/compute/providers/:name", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "DELETE", Path: "/api/compute/providers/:name", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/providers/:name/probe", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/providers/:name/data-roots", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/providers/:name/scratch-root", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/ssh-hosts", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/inference-providers", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "DELETE", Path: "/api/compute/inference-providers/:name", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/managed-endpoints", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/managed-endpoints/:name/stop", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/byoc/:provider", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/byoc/:provider/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/jobs", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/jobs/:jobId", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/jobs/:jobId/logs", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/ssh-config-aliases", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/session/:rootFrameId/enabled", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "PUT", Path: "/api/compute/session/:rootFrameId/enabled/:name", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/session/migrate", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/providers/:name/files", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/providers/:name/import", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/providers/:name/download", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/local/files", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/local/download", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "GET", Path: "/api/compute/local/hostinfo", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
	{Method: "POST", Path: "/api/compute/local/import", SourceFile: computeRouteSource, SourceLine: computeRouteSourceLine},
}
