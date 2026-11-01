package piercore

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"golang.org/x/crypto/ssh"
)

func instanceWith(state ec2types.InstanceStateName, tags map[string]string) ec2types.Instance {
	i := ec2types.Instance{
		InstanceId: aws.String("i-1"), InstanceType: "t4g.medium",
		State:           &ec2types.InstanceState{Name: state},
		PublicIpAddress: aws.String("203.0.113.7"),
		Placement:       &ec2types.Placement{AvailabilityZone: aws.String("eu-central-1a")},
		SecurityGroups:  []ec2types.GroupIdentifier{{GroupId: aws.String("sg-1")}},
	}
	for k, v := range tags {
		i.Tags = append(i.Tags, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return i
}

func TestSessionFromReadsPiersTags(t *testing.T) {
	s, ok := sessionFrom(instanceWith(ec2types.InstanceStateNameRunning, map[string]string{
		tagSession: "fix-login", tagRepo: "shop", tagBranch: "fix-login", tagReady: "1",
		tagOrigin: "github.com/acme/shop", tagBase: "main", tagCreated: "2026-10-01T10:00:00Z",
	}))
	if !ok {
		t.Fatal("a session wasn't read")
	}
	want := session{
		ID: "i-1", Name: "fix-login", Repo: "shop", Origin: "github.com/acme/shop", Base: "main",
		Branch: "fix-login", State: "running", MachineType: "t4g.medium", CreatedAt: "2026-10-01T10:00:00Z",
	}
	if s.Model != want {
		t.Fatalf("session = %+v, want %+v", s.Model, want)
	}
	if s.Host != "203.0.113.7" || s.Zone != "eu-central-1a" || s.SecurityGroup != "sg-1" {
		t.Fatalf("reachability = %q %q %q", s.Host, s.Zone, s.SecurityGroup)
	}
}

func TestSessionFromStates(t *testing.T) {
	for _, c := range []struct {
		state ec2types.InstanceStateName
		ready string
		want  string
	}{
		{ec2types.InstanceStateNamePending, "", "creating"},
		{ec2types.InstanceStateNameRunning, "", "creating"},
		{ec2types.InstanceStateNameRunning, "1", "running"},
		{ec2types.InstanceStateNameStopping, "1", "parked"},
		{ec2types.InstanceStateNameStopped, "1", "parked"},
	} {
		s, _ := sessionFrom(instanceWith(c.state, map[string]string{tagSession: "x", tagReady: c.ready}))
		if s.Model.State != c.want {
			t.Errorf("%s ready=%q: state %q, want %q", c.state, c.ready, s.Model.State, c.want)
		}
	}
}

func TestSessionFromSkipsUnclaimedPoolMembers(t *testing.T) {
	if _, ok := sessionFrom(instanceWith(ec2types.InstanceStateNameStopped, map[string]string{tagPool: "abc"})); ok {
		t.Fatal("a warm-pool member was listed as a session")
	}
}

func TestParseInspectionReadsTheBeacon(t *testing.T) {
	out := `{"state":"working","strained":true,"setup":"done","ports":[{"port":3000,"process":"node","pid":7}],` +
		`"res":{"cpu":42.5,"mem_used":1,"mem_total":4},"park_at":"2026-11-01T12:00:00Z","renamed":"add-login",` +
		`"chats":[{"id":"c1","agent":"claude","title":"Add login","state":"waiting","waiting":"permission","since":"2026-11-01T11:00:00Z"}]}
` + inspectSep + `
@1|0|0|zsh|home|zsh
@4|1|1|node|agent|claude: Add login
`
	got := parseInspection(session{ID: "i-1", State: "running", Branch: "pier-1"}, out)
	if got.Session.State != "working" || got.Session.Branch != "add-login" {
		t.Fatalf("session = %+v", got.Session)
	}
	if !got.Strained || got.Setup != "done" || got.CPU != 42.5 || got.Memory != 25 || got.ParkAt == nil {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0] != (port{Port: 3000, Process: "node"}) {
		t.Fatalf("ports = %+v", got.Ports)
	}
	if len(got.Chats) != 1 || got.Chats[0].Waiting != "permission" {
		t.Fatalf("chats = %+v", got.Chats)
	}
	want := []window{
		{ID: "@1", Index: 0, Command: "zsh", Role: "home", Name: "zsh"},
		{ID: "@4", Index: 1, Active: true, Command: "node", Role: "agent", Name: "claude: Add login"},
	}
	if len(got.Windows) != 2 || got.Windows[0] != want[0] || got.Windows[1] != want[1] {
		t.Fatalf("windows = %+v", got.Windows)
	}
}

func TestParseInspectionWithoutABeacon(t *testing.T) {
	got := parseInspection(session{State: "running", Branch: "b"}, "{}\n"+inspectSep+"\n")
	if got.Session.State != "running" || got.Session.Branch != "b" || got.Ports == nil || got.Chats == nil || got.Windows == nil {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestTabAttachUsesAGroupedTabSession(t *testing.T) {
	script, err := tabAttach("@4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(script, ensureMain+"\n") {
		t.Fatalf("main isn't ensured first:\n%s", script)
	}
	for _, want := range []string{"tmux new-session -d -t main -s pier-tab-ios", ":@4 2>/dev/null", "destroy-unattached on"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q:\n%s", want, script)
		}
	}
	if _, err := tabAttach("@4; rm -rf ~"); err == nil {
		t.Fatal("a bad window id was accepted")
	}
}

func TestChatWindowScript(t *testing.T) {
	got := chatWindowScript("c1", true)
	if !strings.HasPrefix(got, "grep -q '\"chats\"' /run/pier/status.json") {
		t.Fatalf("no guard against a supervisor that predates chats:\n%s", got)
	}
	if !strings.HasSuffix(got, "\npier-supervisor chat window --takeover 'c1'") {
		t.Fatalf("script = %s", got)
	}
	if strings.Contains(chatWindowScript("", false), "--takeover") {
		t.Fatal("watching took over")
	}
}

func TestRuleUserMatchesTheMacs(t *testing.T) {
	got := ruleUser("arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Dev_abc/alice+x@example.com")
	if got != "alice-x@example.com" {
		t.Fatalf("ruleUser = %q", got)
	}
}

func TestDialSSHTimesOutWhenServerNeverSendsBanner(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	started := time.Now()
	_, err = dialSSH(context.Background(), listener.Addr().String(), &ssh.ClientConfig{
		User:            "agent",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("dialSSH unexpectedly completed an SSH handshake")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SSH banner timeout took %s", elapsed)
	}
	select {
	case connection := <-accepted:
		_ = connection.Close()
	default:
	}
}

func TestFreshClientIsNotSignedIn(t *testing.T) {
	c, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	var got status
	raw, _ := c.Status()
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.SignedIn || got.Ready {
		t.Fatalf("status = %+v", got)
	}
}

func TestSignOutForgetsEverything(t *testing.T) {
	c, _ := NewClient("")
	c.session = sessionState{AccessToken: "token", AccountID: "123456789012", RoleName: "Developer"}
	c.pending = &pendingAuthorization{ClientID: "pending"}
	c.sessions["i-1"] = remoteSession{Model: session{ID: "i-1"}}
	c.ensured["sg-1"] = "203.0.113.7/32"
	if err := c.SignOut(); err != nil {
		t.Fatal(err)
	}
	if c.pending != nil || len(c.sessions) != 0 || len(c.remotes) != 0 || len(c.ensured) != 0 || c.config != nil {
		t.Fatalf("sign-out kept state: %+v", c)
	}
	var got status
	raw, _ := c.Status()
	_ = json.Unmarshal([]byte(raw), &got)
	if got.SignedIn {
		t.Fatal("still signed in")
	}
}

func TestAuthenticationRequiredErrorIsStableAndUserFacing(t *testing.T) {
	message := authenticationRequiredError().Error()
	if !strings.HasPrefix(message, authenticationRequiredErrorMark) {
		t.Fatalf("authentication error = %q, want stable marker", message)
	}
	if !strings.Contains(message, "Sign in again to reconnect Pier") {
		t.Fatalf("authentication error = %q, want reconnect guidance", message)
	}
}

func TestSelectionForReauthorizationKeepsExistingAccountAndRole(t *testing.T) {
	request := signInRequest{
		StartURL: "https://example.awsapps.com/start", SSORegion: "eu-central-1", AWSRegion: "eu-west-1",
	}
	previous := sessionState{Request: request, AccountID: "123456789012", RoleName: "Developer"}
	current := sessionState{Request: request, AccessToken: "new-access-token"}

	got, ok := selectionForReauthorization(previous, current, []account{{ID: previous.AccountID}})
	if !ok {
		t.Fatal("selectionForReauthorization() did not preserve a valid selection")
	}
	if got.AccountID != previous.AccountID || got.RoleName != previous.RoleName {
		t.Fatalf("selection = %q · %q, want %q · %q", got.AccountID, got.RoleName, previous.AccountID, previous.RoleName)
	}
}
