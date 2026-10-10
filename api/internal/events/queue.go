package events

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Message is one queue message.
type Message struct {
	ID           string
	Body         []byte
	Receipt      string // handle to delete it with
	ReceiveCount int    // how many times it's been received, this one included
}

// Queue is the subset of SQS the reader needs.
type Queue interface {
	// Receive long-polls for up to max messages.
	Receive(ctx context.Context, max int) ([]Message, error)
	// Delete removes a message that's been handled.
	Delete(ctx context.Context, receipt string) error
}

// --- SQS ---

type SQS struct {
	client *sqs.Client
	url    string
}

// NewSQS uses the default credential chain, like the SES client.
func NewSQS(ctx context.Context, region, queueURL string) (*SQS, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &SQS{client: sqs.NewFromConfig(cfg), url: queueURL}, nil
}

func (q *SQS) Receive(ctx context.Context, max int) ([]Message, error) {
	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(q.url),
		MaxNumberOfMessages:         int32(max),
		WaitTimeSeconds:             20,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		n, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		msgs = append(msgs, Message{ID: aws.ToString(m.MessageId), Body: []byte(aws.ToString(m.Body)), Receipt: aws.ToString(m.ReceiptHandle), ReceiveCount: n})
	}
	return msgs, nil
}

func (q *SQS) Delete(ctx context.Context, receipt string) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(q.url), ReceiptHandle: aws.String(receipt)})
	return err
}

// --- Fake, for tests and development ---

// Fake behaves like SQS with a redrive policy: a received message that isn't
// deleted comes back, and after MaxReceives it moves to DLQ.
type Fake struct {
	MaxReceives int // default 5, like infra/aws/events.tf

	mu       sync.Mutex
	next     int
	visible  []*fakeMsg
	inflight map[string]*fakeMsg
	dlq      []Message
}

type fakeMsg struct {
	Message
	receives int
}

// Push adds a message body.
func (f *Fake) Push(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.visible = append(f.visible, &fakeMsg{Message: Message{ID: strconv.Itoa(f.next), Body: body}})
}

func (f *Fake) Receive(ctx context.Context, max int) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inflight == nil {
		f.inflight = map[string]*fakeMsg{}
	}
	limit := f.MaxReceives
	if limit == 0 {
		limit = 5
	}
	// Messages not deleted since the last receive become visible again
	// (the visibility timeout ran out).
	for r, m := range f.inflight {
		delete(f.inflight, r)
		if m.receives >= limit {
			f.dlq = append(f.dlq, m.Message)
			continue
		}
		f.visible = append(f.visible, m)
	}
	var out []Message
	for len(f.visible) > 0 && len(out) < max {
		m := f.visible[0]
		f.visible = f.visible[1:]
		m.receives++
		m.Receipt = m.ID + "/" + strconv.Itoa(m.receives)
		m.ReceiveCount = m.receives
		f.inflight[m.Receipt] = m
		out = append(out, m.Message)
	}
	if len(out) == 0 {
		select { // long poll, briefly
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	return out, nil
}

func (f *Fake) Delete(_ context.Context, receipt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.inflight, receipt)
	return nil
}

// Pending: messages still on the queue (visible or in flight).
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.visible) + len(f.inflight)
}

// DeadLetters: messages moved to the dead-letter queue.
func (f *Fake) DeadLetters() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.dlq...)
}
