package jobs

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestProposalJobErrorUsesContractBounds(t *testing.T) {
	detail := jobError(rinruntime.NewFieldError(
		strings.Repeat("a", protocol.ErrorCodeMaxLength+20),
		strings.Repeat("界", protocol.ErrorMessageMaxLength+20),
		strings.Repeat("f", protocol.ErrorFieldMaxLength+20),
		nil,
	))
	if utf8.RuneCountInString(detail.Code) != protocol.ErrorCodeMaxLength ||
		utf8.RuneCountInString(detail.Message) != protocol.ErrorMessageMaxLength ||
		utf8.RuneCountInString(detail.Field) != protocol.ErrorFieldMaxLength {
		t.Fatalf("proposal job error exceeded the wire contract: %+v", detail)
	}
}
