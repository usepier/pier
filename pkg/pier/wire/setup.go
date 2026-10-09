package wire

// SetupTool is a command-line tool setup needs.
type SetupTool struct {
	Name  string `json:"name"`
	Hint  string `json:"hint"` // how to install it
	Found bool   `json:"found"`
}

// SetupDraft is setup's answers: the cloud, its account, and the defaults
// new sessions get.
type SetupDraft struct {
	Cloud      string `json:"cloud"` // aws-ec2 | gcp-gce
	AWSProfile string `json:"awsProfile,omitempty"`
	AWSRegion  string `json:"awsRegion,omitempty"`
	// AWSSubnet is for accounts without a default VPC.
	AWSSubnet     string   `json:"awsSubnet,omitempty"`
	GCPProject    string   `json:"gcpProject,omitempty"`
	GCPZone       string   `json:"gcpZone,omitempty"`
	Machine       string   `json:"machine"`
	DiskGiB       int      `json:"diskGib"`
	IdleTimeout   string   `json:"idleTimeout"`
	UnattendedCap string   `json:"unattendedCap"`
	Speed         string   `json:"speed"` // fast | lean | minimal | custom
	Manifest      []string `json:"manifest"`
	// ClaudeToken is set only to change it; it is never read back.
	ClaudeToken string `json:"claudeToken,omitempty"`
}

// SetupState is what setup finds on this machine before asking anything.
type SetupState struct {
	Configured bool        `json:"configured"`
	Draft      SetupDraft  `json:"draft"` // the saved answers, else the defaults
	AWSTools   []SetupTool `json:"awsTools"`
	GCPTools   []SetupTool `json:"gcpTools"`
	// AWSProfiles are the AWS CLI's profiles; GCPAccount and GCPProject
	// gcloud's active login and project.
	AWSProfiles []string `json:"awsProfiles"`
	GCPAccount  string   `json:"gcpAccount,omitempty"`
	GCPProject  string   `json:"gcpProject,omitempty"`
	// Agents are the agent config dirs here (".claude", ".codex"): the
	// onboarding skill can go into each.
	Agents []string `json:"agents"`
	// ClaudeAuth says how claude authenticates without a token, if it can;
	// NeedsClaudeToken that sessions need one (claude setup-token).
	ClaudeAuth       string `json:"claudeAuth,omitempty"`
	NeedsClaudeToken bool   `json:"needsClaudeToken"`
	HasClaudeToken   bool   `json:"hasClaudeToken"`
}

// SetupCheckParams checks a cloud login.
type SetupCheckParams struct {
	Cloud   string `json:"cloud"`
	Profile string `json:"profile,omitempty"` // aws
	Project string `json:"project,omitempty"` // gcp
	// MFACode answers an AWS profile that asks for one (kind mfa_required).
	MFACode string `json:"mfaCode,omitempty"`
}

// RegionsParams asks where sessions can run: AWS regions for a profile,
// or GCP zones for a project.
type RegionsParams struct {
	Cloud   string `json:"cloud"`
	Profile string `json:"profile,omitempty"`
	Project string `json:"project,omitempty"`
}

// Regions are the regions (AWS) or zones (GCP) to pick from.
type Regions struct {
	Regions []string `json:"regions"`
}

// SetupIdentity is who the login is, and the region the AWS profile uses.
type SetupIdentity struct {
	Identity string `json:"identity"`
	Region   string `json:"region,omitempty"`
}

// DefaultRow is one default new sessions get.
type DefaultRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// SetupDefaults is what a draft gives new sessions, and the machines to
// pick from.
type SetupDefaults struct {
	Rows     []DefaultRow `json:"rows"`
	Machines []Machine    `json:"machines"`
}

// SetupApplyParams finishes setup.
type SetupApplyParams struct {
	Draft SetupDraft `json:"draft"`
	// Skills are the agent dirs to put the onboarding skill in.
	Skills []string `json:"skills"`
}

// SetupResult is what setup did.
type SetupResult struct {
	Created []string          `json:"created"`
	Existed []string          `json:"existed"`
	Checks  []Check           `json:"checks"`
	Skills  []string          `json:"skills"`
	Adopted map[string]string `json:"adopted"` // repo → session image found in the account
}

// CloudRef names a cloud.
type CloudRef struct {
	Cloud string `json:"cloud"`
}
