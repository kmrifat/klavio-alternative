package ses

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/aws/smithy-go"
)

// Only answers that certainly mean "not sent" may be retried; anything that
// could have come after SES accepted the message must not be.
func TestRetryableOnlyWhenCertainlyNotSent(t *testing.T) {
	api := func(code string) error { return &smithy.GenericAPIError{Code: code, Message: "x"} }
	cases := []struct {
		err   error
		retry bool
	}{
		{api("TooManyRequestsException"), true},
		{api("ThrottlingException"), true},
		{api("ServiceUnavailable"), true},
		{api("MessageRejected"), false},
		{api("SendingPausedException"), false},
		{api("InternalFailure"), false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("operation error SESv2: SendEmail: %w", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), false},
	}
	for _, c := range cases {
		if got := Retryable(classify(c.err)); got != c.retry {
			t.Errorf("%v: retryable %v, want %v", c.err, got, c.retry)
		}
	}
}
