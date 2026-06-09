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
)
