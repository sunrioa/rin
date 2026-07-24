package protocol_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
)

func TestNewErrorDetailEnforcesGeneratedWireBounds(t *testing.T) {
	detail := protocol.NewErrorDetail(
		strings.Repeat("a", protocol.ErrorCodeMaxLength+20),
		strings.Repeat("界", protocol.ErrorMessageMaxLength+20),
		strings.Repeat("f", protocol.ErrorFieldMaxLength+20),
	)
	if utf8.RuneCountInString(detail.Code) != protocol.ErrorCodeMaxLength ||
		utf8.RuneCountInString(detail.Message) != protocol.ErrorMessageMaxLength ||
		utf8.RuneCountInString(detail.Field) != protocol.ErrorFieldMaxLength {
		t.Fatalf("error detail was not bounded by the generated contract: %+v", detail)
	}

	invalid := protocol.NewErrorDetail("internal_error", "bad\xffmessage", "bad\xfffield")
	if !utf8.ValidString(invalid.Message) || !utf8.ValidString(invalid.Field) {
		t.Fatalf("error detail retained invalid UTF-8: %+v", invalid)
	}
	if invalidCode := protocol.NewErrorDetail("Bad-Code", "bad code", ""); invalidCode.Code != "internal_error" {
		t.Fatalf("invalid error code was exposed on the wire: %+v", invalidCode)
	}
}
