// Package piercore exposes Pier's AWS and remote-session operations through a
// gomobile-compatible API. The public surface deliberately uses strings,
// byte slices, and small interfaces so the generated Apple framework stays
// stable while the native Go implementation can evolve.
package piercore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type signInRequest struct {
	StartURL  string `json:"startURL"`
	SSORegion string `json:"ssoRegion"`
	AWSRegion string `json:"awsRegion"`
}

type sessionState struct {
	Request               signInRequest `json:"request"`
	ClientID              string        `json:"clientID"`
	ClientSecret          string        `json:"clientSecret"`
	ClientSecretExpiresAt int64         `json:"clientSecretExpiresAt"`
	AccessToken           string        `json:"accessToken"`
	AccessTokenExpiresAt  int64         `json:"accessTokenExpiresAt"`
	RefreshToken          string        `json:"refreshToken"`
	AccountID             string        `json:"accountID"`
	RoleName              string        `json:"roleName"`
}

type pendingAuthorization struct {
	Request               signInRequest
	ClientID              string
	ClientSecret          string
	ClientSecretExpiresAt int64
	DeviceCode            string
	ExpiresAt             time.Time
	Interval              time.Duration
}

type authorization struct {
	VerificationURL string  `json:"verificationURL"`
	UserCode        string  `json:"userCode"`
	ExpiresAt       float64 `json:"expiresAt"`
}

type account struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type role struct {
	Name string `json:"name"`
}

type setupStatus struct {
	Configured   bool         `json:"configured"`
	ConfigPath   string       `json:"configPath"`
	CLIVersion   string       `json:"cliVersion"`
	Profiles     []string     `json:"profiles"`
	Dependencies []dependency `json:"dependencies"`
}

type dependency struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type instance struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	User         string `json:"user"`
	Driver       string `json:"driver"`
	State        string `json:"state"`
	Strained     bool   `json:"strained"`
	Setup        string `json:"setup"`
	InstanceType string `json:"instanceType"`
	CreatedAt    string `json:"createdAt"`
	CostNote     string `json:"costNote"`
	LocalPath    string `json:"localPath,omitempty"`
	ProjectID    string `json:"projectID,omitempty"`
	Repository   string `json:"repository,omitempty"`
}

type remoteInstance struct {
	Model            instance
	Host             string
	AvailabilityZone string
	SecurityGroupID  string
}

type project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Repository string `json:"repository,omitempty"`
	Host       string `json:"host"`
}

type branchOptions struct {
	Project       project  `json:"project"`
	Branches      []string `json:"branches"`
	DefaultBranch string   `json:"defaultBranch"`
	FetchWarning  string   `json:"fetchWarning,omitempty"`
}

type tab struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Active           bool   `json:"active"`
	Panes            int    `json:"panes"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
}

type port struct {
	Number  int    `json:"number"`
	Process string `json:"process,omitempty"`
	IsHTTP  bool   `json:"isHTTP"`
}

type snapshot struct {
	Instance instance `json:"instance"`
	Tabs     []tab    `json:"tabs"`
	Ports    []port   `json:"ports"`
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode Pier response: %w", err)
	}
	return string(data), nil
}

func projectName(repo string) string {
	name := filepath.Base(strings.TrimSuffix(repo, "/"))
	if name == "." || name == "/" || name == "" {
		return repo
	}
	return name
}

func projectsFromInstances(instances []instance) []project {
	seen := make(map[string]bool)
	projects := make([]project, 0)
	for _, item := range instances {
		name := projectName(item.Repo)
		id := "aws:" + name
		if seen[id] {
			if item.Repository != "" {
				for index := range projects {
					if projects[index].ID == id && projects[index].Repository == "" {
						projects[index].Repository = item.Repository
					}
				}
			}
			continue
		}
		seen[id] = true
		projects = append(projects, project{
			ID: id, Name: name, Path: "AWS", Repository: item.Repository, Host: "AWS",
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name)
	})
	return projects
}
