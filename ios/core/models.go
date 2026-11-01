package piercore

// models.go: what the core hands Swift, as JSON (gomobile carries strings,
// not structs), and the sign-in state it keeps.

import "time"

type signInRequest struct {
	StartURL  string `json:"startURL"`
	SSORegion string `json:"ssoRegion"`
	AWSRegion string `json:"awsRegion"`
}

// sessionState is the sign-in, kept in the iOS Keychain between launches.
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

type status struct {
	SignedIn  bool   `json:"signedIn"`
	Ready     bool   `json:"ready"` // an account and permission set picked
	AccountID string `json:"accountID,omitempty"`
	RoleName  string `json:"roleName,omitempty"`
	Region    string `json:"region,omitempty"`
	StartURL  string `json:"startURL,omitempty"`
}

// session is one of the user's pier sessions.
type session struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Repo        string `json:"repo"`
	Origin      string `json:"origin,omitempty"` // host/owner/repo
	Base        string `json:"base,omitempty"`
	Branch      string `json:"branch"`
	State       string `json:"state"` // creating, running, working, idle, parked
	MachineType string `json:"machineType"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

type remoteSession struct {
	Model         session
	Host          string
	Zone          string
	SecurityGroup string
}

// window is a tmux window of the session's main.
type window struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
	Role    string `json:"role,omitempty"` // home, setup, agent, …
	Name    string `json:"name"`
}

type port struct {
	Port    int    `json:"port"`
	Process string `json:"process"`
}

// chat is one of the session's agent chats as the supervisor sums it up
// (wire.ChatSummary).
type chat struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Title   string    `json:"title"`
	State   string    `json:"state"`
	Waiting string    `json:"waiting,omitempty"`
	Since   time.Time `json:"since"`
	Last    string    `json:"last,omitempty"`
	Ask     string    `json:"ask,omitempty"`
	Queued  int       `json:"queued,omitempty"`
}

// snapshot is what a running session is doing.
type snapshot struct {
	Session  session    `json:"session"`
	Windows  []window   `json:"windows"`
	Ports    []port     `json:"ports"`
	Chats    []chat     `json:"chats"`
	Strained bool       `json:"strained"`
	Setup    string     `json:"setup,omitempty"`
	CPU      float64    `json:"cpu"`
	Memory   float64    `json:"memory"` // % used
	ParkAt   *time.Time `json:"parkAt,omitempty"`
	CapAt    *time.Time `json:"capAt,omitempty"`
}
