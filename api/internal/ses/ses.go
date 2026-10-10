// Package ses sets up Amazon SES for a brand: a configuration set that
// publishes events to EventBridge, and a verified domain identity with
// Easy DKIM and a custom MAIL FROM domain.
package ses

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Identity statuses, as SES reports them.
const (
	StatusPending = "PENDING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
)

type DomainSetup struct {
	DKIMTokens     []string // each becomes <token>._domainkey.<domain> CNAME <token>.dkim.amazonses.com
	IdentityStatus string
}

type Provisioner interface {
	// SetUpBrand is idempotent: re-running it for an existing brand is safe.
	SetUpBrand(ctx context.Context, domain, mailFromDomain, configurationSet string) (DomainSetup, error)
	IdentityStatus(ctx context.Context, domain string) (string, error)
	Region() string
}

// --- AWS ---

type AWS struct {
	client      *sesv2.Client
	region      string
	eventBusARN string
}

// NewAWS uses the default credential chain (IAM role, env vars, ~/.aws).
func NewAWS(ctx context.Context, region, eventBusARN string) (*AWS, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &AWS{client: sesv2.NewFromConfig(cfg), region: region, eventBusARN: eventBusARN}, nil
}

func (a *AWS) Region() string { return a.region }

func alreadyExists(err error) bool {
	var ae *types.AlreadyExistsException
	return errors.As(err, &ae)
}

func (a *AWS) SetUpBrand(ctx context.Context, domain, mailFromDomain, configurationSet string) (DomainSetup, error) {
	_, err := a.client.CreateConfigurationSet(ctx, &sesv2.CreateConfigurationSetInput{
		ConfigurationSetName: aws.String(configurationSet),
		ReputationOptions:    &types.ReputationOptions{ReputationMetricsEnabled: true},
		SendingOptions:       &types.SendingOptions{SendingEnabled: true},
	})
	if err != nil && !alreadyExists(err) {
		return DomainSetup{}, fmt.Errorf("create configuration set: %w", err)
	}

	_, err = a.client.CreateConfigurationSetEventDestination(ctx, &sesv2.CreateConfigurationSetEventDestinationInput{
		ConfigurationSetName: aws.String(configurationSet),
		EventDestinationName: aws.String("eventbridge"),
		EventDestination: &types.EventDestinationDefinition{
			Enabled: true,
			MatchingEventTypes: []types.EventType{
				types.EventTypeSend, types.EventTypeReject, types.EventTypeBounce, types.EventTypeComplaint,
				types.EventTypeDelivery, types.EventTypeOpen, types.EventTypeClick,
				types.EventTypeRenderingFailure, types.EventTypeDeliveryDelay, types.EventTypeSubscription,
			},
			EventBridgeDestination: &types.EventBridgeDestination{EventBusArn: aws.String(a.eventBusARN)},
		},
	})
	if err != nil && !alreadyExists(err) {
		return DomainSetup{}, fmt.Errorf("create event destination: %w", err)
	}

	var setup DomainSetup
	out, err := a.client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
		EmailIdentity:         aws.String(domain),
		ConfigurationSetName:  aws.String(configurationSet),
		DkimSigningAttributes: &types.DkimSigningAttributes{NextSigningKeyLength: types.DkimSigningKeyLengthRsa2048Bit},
	})
	switch {
	case err == nil:
		if out.DkimAttributes != nil {
			setup.DKIMTokens = out.DkimAttributes.Tokens
		}
		setup.IdentityStatus = StatusPending
	case alreadyExists(err):
		got, err := a.client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
		if err != nil {
			return DomainSetup{}, fmt.Errorf("get email identity: %w", err)
		}
		if got.DkimAttributes != nil {
			setup.DKIMTokens = got.DkimAttributes.Tokens
		}
		setup.IdentityStatus = string(got.VerificationStatus)
	default:
		return DomainSetup{}, fmt.Errorf("create email identity: %w", err)
	}

	_, err = a.client.PutEmailIdentityMailFromAttributes(ctx, &sesv2.PutEmailIdentityMailFromAttributesInput{
		EmailIdentity:       aws.String(domain),
		MailFromDomain:      aws.String(mailFromDomain),
		BehaviorOnMxFailure: types.BehaviorOnMxFailureUseDefaultValue,
	})
	if err != nil {
		return DomainSetup{}, fmt.Errorf("set MAIL FROM: %w", err)
	}
	return setup, nil
}

func (a *AWS) IdentityStatus(ctx context.Context, domain string) (string, error) {
	got, err := a.client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
	if err != nil {
		return "", err
	}
	return string(got.VerificationStatus), nil
}

// --- Fake, for development and tests ---

// Fake hands out deterministic DKIM tokens and never talks to AWS. Identities
// start PENDING and report SUCCESS on the next status check (there is nothing
// to verify), unless a test sets another status.
type Fake struct {
	mu     sync.Mutex
	status map[string]string
}

func (f *Fake) Region() string { return "us-east-1" }

func (f *Fake) SetUpBrand(_ context.Context, domain, _, _ string) (DomainSetup, error) {
	tokens := make([]string, 3)
	for i := range tokens {
		h := sha256.Sum256(fmt.Appendf(nil, "%s/%d", domain, i))
		tokens[i] = hex.EncodeToString(h[:16])
	}
	return DomainSetup{DKIMTokens: tokens, IdentityStatus: StatusPending}, nil
}

func (f *Fake) IdentityStatus(_ context.Context, domain string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.status[domain]; ok {
		return s, nil
	}
	return StatusSuccess, nil
}

func (f *Fake) SetStatus(domain, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == nil {
		f.status = map[string]string{}
	}
	f.status[domain] = status
}
