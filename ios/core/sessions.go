package piercore

// sessions.go: the signed-in user's pier sessions as EC2 lists them, found
// by the same tags the Mac's pier writes (internal/driver/awsec2), and the
// three things the phone does to one: wake, park, remove.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// The tags pier puts on a session's instance (awsec2's Tag* constants).
const (
	tagManaged = "pier:managed"
	tagUser    = "pier:user"
	tagSession = "pier:session"
	tagRepo    = "pier:repo"
	tagBranch  = "pier:branch"
	tagReady   = "pier:ready"
	tagCreated = "pier:created"
	tagPool    = "pier:pool"
	tagOrigin  = "pier:origin"
	tagBase    = "pier:base"
)

// callerARN is who the role credentials are: the complete ARN, role-session
// name included. pier tags a session with exactly that, so two people on the
// same permission set never see each other's sessions.
func callerARN(ctx context.Context, config aws.Config) (string, error) {
	identity, err := sts.NewFromConfig(config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("get AWS caller identity: %w", err)
	}
	return aws.ToString(identity.Arn), nil
}

func loadSessions(ctx context.Context, config aws.Config, me string) ([]remoteSession, error) {
	input := &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:" + tagManaged), Values: []string{"1"}},
		{Name: aws.String("tag:" + tagUser), Values: []string{me}},
		{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
	}}
	pages := ec2.NewDescribeInstancesPaginator(ec2.NewFromConfig(config), input)
	items := []remoteSession{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list pier sessions: %w", err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				if s, ok := sessionFrom(i); ok {
					items = append(items, s)
				}
			}
		}
	}
	sort.Slice(items, func(a, b int) bool {
		if items[a].Model.Repo != items[b].Model.Repo {
			return strings.ToLower(items[a].Model.Repo) < strings.ToLower(items[b].Model.Repo)
		}
		return strings.ToLower(items[a].Model.Name) < strings.ToLower(items[b].Model.Name)
	})
	return items, nil
}

// sessionFrom reads one instance; an unclaimed warm-pool member isn't a
// session of anyone's yet.
func sessionFrom(i ec2types.Instance) (remoteSession, bool) {
	tags := map[string]string{}
	for _, t := range i.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	id := aws.ToString(i.InstanceId)
	if id == "" || tags[tagPool] != "" {
		return remoteSession{}, false
	}
	state := "running"
	if i.State != nil {
		switch i.State.Name {
		case ec2types.InstanceStateNamePending:
			state = "creating"
		case ec2types.InstanceStateNameRunning:
			if tags[tagReady] != "1" {
				state = "creating"
			}
		case ec2types.InstanceStateNameStopping, ec2types.InstanceStateNameStopped:
			state = "parked"
		}
	}
	created := tags[tagCreated]
	if created == "" && i.LaunchTime != nil {
		created = i.LaunchTime.UTC().Format(time.RFC3339)
	}
	name := firstNonempty(tags[tagSession], id)
	s := remoteSession{Model: session{
		ID: id, Name: name, Repo: tags[tagRepo], Origin: tags[tagOrigin], Base: tags[tagBase],
		Branch: firstNonempty(tags[tagBranch], name), State: state,
		MachineType: string(i.InstanceType), CreatedAt: created,
	}}
	s.Host = aws.ToString(i.PublicIpAddress)
	if i.Placement != nil {
		s.Zone = aws.ToString(i.Placement.AvailabilityZone)
	}
	if len(i.SecurityGroups) > 0 {
		s.SecurityGroup = aws.ToString(i.SecurityGroups[0].GroupId)
	}
	return s, true
}

func startInstance(ctx context.Context, config aws.Config, id string) error {
	client := ec2.NewFromConfig(config)
	if _, err := client.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("wake the session: %w", err)
	}
	waiter := ec2.NewInstanceRunningWaiter(client)
	if err := waiter.Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 4*time.Minute); err != nil {
		return fmt.Errorf("wait for the session to wake: %w", err)
	}
	return nil
}

// stopInstance parks: a stop, as the Mac's park is. The session's
// supervisor saves its tmux layout on the way down.
func stopInstance(ctx context.Context, config aws.Config, id string) error {
	if _, err := ec2.NewFromConfig(config).StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("park the session: %w", err)
	}
	return nil
}

func terminateInstance(ctx context.Context, config aws.Config, id string) error {
	if _, err := ec2.NewFromConfig(config).TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("remove the session: %w", err)
	}
	return nil
}
