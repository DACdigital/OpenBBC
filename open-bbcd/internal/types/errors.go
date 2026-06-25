package types

import "errors"

var (
	ErrNameRequired   = errors.New("name is required")
	ErrPromptRequired = errors.New("prompt is required")
	ErrAgentRequired  = errors.New("agent_id is required")
	ErrNotFound       = errors.New("not found")

	ErrDiscoveryFileRequired     = errors.New("discovery file is required")
	ErrDiscoveryFileTooLarge     = errors.New("discovery file is too large")
	ErrDiscoveryFileBadExtension = errors.New("discovery file must be a .zip")

	ErrFlowMapInvalid = errors.New("flow-map archive is invalid")

	ErrSkillReferenced         = errors.New("skill is referenced by a flow's workflow and cannot be deleted")
	ErrCapabilityReadOnly      = errors.New("capabilities are read-only")
	ErrInvalidSkillRole        = errors.New("skill role must be 'read' or 'write'")
	ErrCustomSkillNameRequired = errors.New("custom skill name is required")

	ErrInvalidAgentStatus  = errors.New("agent is not in a valid status for this transition")
	ErrBundleAlreadySet    = errors.New("agent: bundle already set")
	ErrAgentNotRunnable    = errors.New("agent: no bundle generated")

	ErrSessionAgentMismatch = errors.New("session: belongs to different agent")

	ErrLLMUnavailable    = errors.New("llm: upstream unavailable")
	ErrToolHandlerFailed = errors.New("tools: handler failed")

	// ErrAgentNotDeployable is returned when /deploy is called on a version
	// that is not in READY status (or already DEPLOYED, which is a no-op
	// handled separately).
	ErrAgentNotDeployable = errors.New("agent: version cannot be deployed (must be READY)")

	// ErrAgentNotDeployed is returned when /undeploy is called on a version
	// that is not currently DEPLOYED.
	ErrAgentNotDeployed = errors.New("agent: version is not deployed")

	// ErrUserIDRequired is returned by deployed-runtime endpoints when the
	// integrator-supplied user_id is missing from the request.
	ErrUserIDRequired = errors.New("deployed: user_id is required")

	ErrToolBackendNameRequired = errors.New("tool backend name is required")
	ErrToolBackendNameTaken    = errors.New("tool backend name already exists")
	ErrToolBackendKindInvalid  = errors.New("tool backend kind must be http_endpoint or mcp_client")
	ErrToolBackendInUse        = errors.New("tool backend is in use by one or more agent versions")

	// ErrUnmappedEndpoints is returned when /deploy is called but the version's
	// bundle contains tool endpoints that have no backend assigned in
	// agent_version_endpoint_backend.
	ErrUnmappedEndpoints = errors.New("agent has endpoints not mapped to a backend")
)
