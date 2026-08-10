package piercore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

func anonymousConfig(region string) aws.Config {
	return aws.Config{Region: region, Credentials: aws.AnonymousCredentials{}}
}

func beginDeviceAuthorization(ctx context.Context, request signInRequest) (*pendingAuthorization, authorization, error) {
	client := ssooidc.NewFromConfig(anonymousConfig(request.SSORegion))
	registered, err := client.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: aws.String("Pier for iOS"),
		ClientType: aws.String("public"),
		GrantTypes: []string{deviceGrant, "refresh_token"},
		Scopes:     []string{"sso:account:access"},
	})
	if err != nil {
		return nil, authorization{}, fmt.Errorf("register Pier with IAM Identity Center: %w", err)
	}
	if aws.ToString(registered.ClientId) == "" || aws.ToString(registered.ClientSecret) == "" {
		return nil, authorization{}, errors.New("AWS did not return an Identity Center client registration")
	}
	started, err := client.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     registered.ClientId,
		ClientSecret: registered.ClientSecret,
		StartUrl:     aws.String(request.StartURL),
	})
	if err != nil {
		return nil, authorization{}, fmt.Errorf("start AWS device authorization: %w", err)
	}
	verificationURL := aws.ToString(started.VerificationUriComplete)
	if verificationURL == "" {
		verificationURL = aws.ToString(started.VerificationUri)
	}
	if aws.ToString(started.DeviceCode) == "" || aws.ToString(started.UserCode) == "" || verificationURL == "" {
		return nil, authorization{}, errors.New("AWS did not return a device authorization URL")
	}
	expiresAt := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	interval := time.Duration(started.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	pending := &pendingAuthorization{
		Request: request, ClientID: aws.ToString(registered.ClientId),
		ClientSecret:          aws.ToString(registered.ClientSecret),
		ClientSecretExpiresAt: registered.ClientSecretExpiresAt,
		DeviceCode:            aws.ToString(started.DeviceCode), ExpiresAt: expiresAt, Interval: interval,
	}
	// Swift's default Codable Date representation is seconds since 2001-01-01.
	appleReference := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	return pending, authorization{
		VerificationURL: verificationURL,
		UserCode:        aws.ToString(started.UserCode),
		ExpiresAt:       expiresAt.Sub(appleReference).Seconds(),
	}, nil
}

func completeDeviceAuthorization(ctx context.Context, pending *pendingAuthorization) (sessionState, error) {
	client := ssooidc.NewFromConfig(anonymousConfig(pending.Request.SSORegion))
	interval := pending.Interval
	for time.Now().Before(pending.ExpiresAt) {
		output, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId: aws.String(pending.ClientID), ClientSecret: aws.String(pending.ClientSecret),
			DeviceCode: aws.String(pending.DeviceCode), GrantType: aws.String(deviceGrant),
		})
		if err == nil {
			if aws.ToString(output.AccessToken) == "" {
				return sessionState{}, errors.New("AWS authorized the device without returning an access token")
			}
			return sessionState{
				Request: pending.Request, ClientID: pending.ClientID, ClientSecret: pending.ClientSecret,
				ClientSecretExpiresAt: pending.ClientSecretExpiresAt,
				AccessToken:           aws.ToString(output.AccessToken),
				AccessTokenExpiresAt:  time.Now().Add(time.Duration(output.ExpiresIn) * time.Second).Unix(),
				RefreshToken:          aws.ToString(output.RefreshToken),
			}, nil
		}
		code := apiErrorCode(err)
		switch code {
		case "AuthorizationPendingException":
			time.Sleep(interval)
		case "SlowDownException":
			interval += 5 * time.Second
			time.Sleep(interval)
		default:
			return sessionState{}, fmt.Errorf("complete AWS device authorization: %w", err)
		}
	}
	return sessionState{}, errors.New("the AWS device code expired; start sign-in again")
}

func refreshAccessToken(ctx context.Context, session sessionState) (sessionState, error) {
	if session.RefreshToken == "" {
		return sessionState{}, errors.New("the AWS session expired; sign in again")
	}
	if session.ClientSecretExpiresAt != 0 && time.Now().Unix() >= session.ClientSecretExpiresAt {
		return sessionState{}, errors.New("the AWS client registration expired; sign in again")
	}
	client := ssooidc.NewFromConfig(anonymousConfig(session.Request.SSORegion))
	output, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{
		ClientId: aws.String(session.ClientID), ClientSecret: aws.String(session.ClientSecret),
		GrantType: aws.String("refresh_token"), RefreshToken: aws.String(session.RefreshToken),
	})
	if err != nil {
		return sessionState{}, fmt.Errorf("refresh AWS sign-in: %w", err)
	}
	session.AccessToken = aws.ToString(output.AccessToken)
	session.AccessTokenExpiresAt = time.Now().Add(time.Duration(output.ExpiresIn) * time.Second).Unix()
	if token := aws.ToString(output.RefreshToken); token != "" {
		session.RefreshToken = token
	}
	return session, nil
}

func listAccounts(ctx context.Context, session sessionState) ([]account, error) {
	client := sso.NewFromConfig(anonymousConfig(session.Request.SSORegion))
	items := make([]account, 0)
	var next *string
	for {
		output, err := client.ListAccounts(ctx, &sso.ListAccountsInput{
			AccessToken: aws.String(session.AccessToken), MaxResults: aws.Int32(100), NextToken: next,
		})
		if err != nil {
			return nil, fmt.Errorf("list AWS accounts: %w", err)
		}
		for _, item := range output.AccountList {
			items = append(items, account{ID: aws.ToString(item.AccountId), Name: aws.ToString(item.AccountName), Email: aws.ToString(item.EmailAddress)})
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		next = output.NextToken
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

func listRoles(ctx context.Context, session sessionState, accountID string) ([]role, error) {
	client := sso.NewFromConfig(anonymousConfig(session.Request.SSORegion))
	items := make([]role, 0)
	var next *string
	for {
		output, err := client.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: aws.String(session.AccessToken), AccountId: aws.String(accountID),
			MaxResults: aws.Int32(100), NextToken: next,
		})
		if err != nil {
			return nil, fmt.Errorf("list AWS permission sets: %w", err)
		}
		for _, item := range output.RoleList {
			items = append(items, role{Name: aws.ToString(item.RoleName)})
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		next = output.NextToken
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

func authenticatedConfig(ctx context.Context, session sessionState) (aws.Config, error) {
	client := sso.NewFromConfig(anonymousConfig(session.Request.SSORegion))
	output, err := client.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
		AccessToken: aws.String(session.AccessToken), AccountId: aws.String(session.AccountID), RoleName: aws.String(session.RoleName),
	})
	if err != nil {
		return aws.Config{}, fmt.Errorf("get AWS role credentials: %w", err)
	}
	if output.RoleCredentials == nil {
		return aws.Config{}, errors.New("AWS did not return role credentials")
	}
	credentials := output.RoleCredentials
	provider := credentials2(aws.ToString(credentials.AccessKeyId), aws.ToString(credentials.SecretAccessKey), aws.ToString(credentials.SessionToken))
	return aws.Config{Region: session.Request.AWSRegion, Credentials: aws.NewCredentialsCache(provider)}, nil
}

func credentials2(accessKey, secretKey, token string) aws.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider(accessKey, secretKey, token)
}

func validateCaller(ctx context.Context, session sessionState) error {
	config, err := authenticatedConfig(ctx, session)
	if err != nil {
		return err
	}
	_, err = sts.NewFromConfig(config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("validate AWS identity: %w", err)
	}
	return nil
}

func loadRemoteInstances(ctx context.Context, session sessionState) ([]remoteInstance, error) {
	config, err := authenticatedConfig(ctx, session)
	if err != nil {
		return nil, err
	}
	identity, err := sts.NewFromConfig(config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("get AWS caller identity: %w", err)
	}
	callerARN := normalizedCallerARN(aws.ToString(identity.Arn))
	input := &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:pier:managed"), Values: []string{"1"}},
		{Name: aws.String("tag:pier:user"), Values: []string{callerARN}},
		{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
	}}
	paginator := ec2.NewDescribeInstancesPaginator(ec2.NewFromConfig(config), input)
	items := make([]remoteInstance, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list Pier instances: %w", err)
		}
		for _, reservation := range page.Reservations {
			for _, value := range reservation.Instances {
				tags := make(map[string]string)
				for _, tag := range value.Tags {
					tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
				}
				id := aws.ToString(value.InstanceId)
				if id == "" {
					continue
				}
				state := "running"
				if value.State != nil {
					switch value.State.Name {
					case ec2types.InstanceStateNamePending:
						state = "creating"
					case ec2types.InstanceStateNameRunning:
						if tags["pier:ready"] != "1" {
							state = "creating"
						}
					case ec2types.InstanceStateNameStopped, ec2types.InstanceStateNameStopping:
						state = "parked"
					}
				}
				repo := firstNonempty(tags["pier:repo"], tags["repo"])
				name := firstNonempty(tags["pier:session"], tags["Name"], id)
				branch := firstNonempty(tags["pier:branch"], name)
				model := instance{
					ID: id, Name: name, Repo: repo, Branch: branch, User: callerARN,
					Driver: "aws-ec2", State: state, Setup: "",
					InstanceType: string(value.InstanceType), CreatedAt: formatTime(value.LaunchTime),
					CostNote: "AWS on-demand", ProjectID: "aws:" + projectName(repo),
					Repository: firstNonempty(tags["pier:repository"], tags["repository"]),
				}
				host := firstNonempty(aws.ToString(value.PublicIpAddress), aws.ToString(value.PublicDnsName))
				zone := ""
				if value.Placement != nil {
					zone = aws.ToString(value.Placement.AvailabilityZone)
				}
				group := ""
				if len(value.SecurityGroups) > 0 {
					group = aws.ToString(value.SecurityGroups[0].GroupId)
				}
				items = append(items, remoteInstance{Model: model, Host: host, AvailabilityZone: zone, SecurityGroupID: group})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Model.Name) < strings.ToLower(items[j].Model.Name)
	})
	return items, nil
}

func (c *Client) loadInstances(ctx context.Context) ([]remoteInstance, error) {
	session, err := c.configuredSession(ctx)
	if err != nil {
		return nil, err
	}
	items, err := loadRemoteInstances(ctx, session)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.instances = make(map[string]remoteInstance, len(items))
	for _, item := range items {
		c.instances[item.Model.ID] = item
	}
	c.mu.Unlock()
	return items, nil
}

func terminateInstance(ctx context.Context, session sessionState, id string) error {
	config, err := authenticatedConfig(ctx, session)
	if err != nil {
		return err
	}
	_, err = ec2.NewFromConfig(config).TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return fmt.Errorf("terminate Pier instance: %w", err)
	}
	return nil
}

func startInstance(ctx context.Context, session sessionState, id string) error {
	config, err := authenticatedConfig(ctx, session)
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(config)
	if _, err := client.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}}); err != nil {
		return fmt.Errorf("start Pier instance: %w", err)
	}
	waiter := ec2.NewInstanceRunningWaiter(client)
	if err := waiter.Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 4*time.Minute); err != nil {
		return fmt.Errorf("wait for Pier instance to start: %w", err)
	}
	return nil
}

func normalizedCallerARN(value string) string {
	parts := strings.Split(value, "/")
	if strings.Contains(value, ":assumed-role/") && len(parts) >= 3 {
		return strings.Join(parts[:2], "/")
	}
	return value
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func apiErrorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}
