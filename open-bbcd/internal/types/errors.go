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

	// ErrAgentInUse is returned when /delete is called on an agent that
	// still has a DEPLOYED version. Undeploy first.
	ErrAgentInUse = errors.New("agent: cannot delete; a version is currently deployed (undeploy first)")

	// ErrVersionInUse is returned when /delete is called on a version that
	// is currently DEPLOYED. Undeploy first.
	ErrVersionInUse = errors.New("version: cannot delete; this version is currently deployed (undeploy first)")

	// ErrVersionHasChildren is returned when /delete is called on a version
	// that has a child (a newer version was forked from it). Versioning is a
	// linked list — deleting a parent would orphan the child's chain.
	ErrVersionHasChildren = errors.New("version: cannot delete; a newer version was forked from this one")

	// ErrAgentNameMismatch is returned when the user-typed confirmation does
	// not match the agent's name on the delete-agent endpoint.
	ErrAgentNameMismatch = errors.New("agent: name confirmation did not match")

	// Feedback / dataset domain (spec: docs/superpowers/specs/2026-07-01-feedback-datasets-design.md).
	ErrFeedbackNotAssistant       = errors.New("feedback: can only attach to assistant messages")
	ErrFeedbackCommentRequired    = errors.New("feedback: comment is required when rating is 'down'")
	ErrDatasetNameRequired        = errors.New("dataset: name is required")
	ErrSessionNoFeedback          = errors.New("dataset: session must have at least one feedback row to be assigned")
	ErrSessionAlreadyInDataset    = errors.New("dataset: session already belongs to another dataset")
	ErrSessionInDataset           = errors.New("dataset: session is pinned inside a closed dataset version and cannot be deleted")
	ErrSessionLocked              = errors.New("session is locked (belongs to a closed dataset version)")
	ErrDatasetVersionClosed       = errors.New("dataset: version is closed and cannot be modified")
)
