package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"mime"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
	"golang.org/x/text/language"
)

const (
	maxMemoryTextBytes = 16 * 1024
	maxSpeechTextBytes = 8 * 1024
	maxQueryTextBytes  = 4 * 1024
	maxSourceEvents    = 64
	maxTags            = 16
	maxMatches         = 64
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
	hashPattern       = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type idField struct {
	name  string
	value string
}

func validateID(field, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must be a 1-96 character identifier", field)
	}
	return nil
}

func validateIDFields(fields ...idField) error {
	for _, field := range fields {
		if err := validateID(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func validateText(field, value string, maximum int) error {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return fmt.Errorf("%s must be nonempty bounded UTF-8", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return fmt.Errorf("%s contains a forbidden control character", field)
		}
	}
	return nil
}

func textHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validateMemoryDocument(document MemoryDocument) error {
	if err := validateIDFields(
		idField{"id", document.ID},
		idField{"session_id", document.SessionID},
		idField{"actor_id", document.ActorID},
	); err != nil {
		return err
	}
	if err := validateText("text", document.Text, maxMemoryTextBytes); err != nil {
		return err
	}
	if !hashPattern.MatchString(document.TextSHA256) ||
		document.TextSHA256 != textHash(document.Text) {
		return errors.New("text_sha256 does not match text")
	}
	if document.StartTick < 0 || document.EndTick < document.StartTick ||
		document.EndTick > protocol.MaxJSONSafeInteger {
		return errors.New("memory tick range is invalid")
	}
	if len(document.SourceEventIDs) == 0 ||
		len(document.SourceEventIDs) > maxSourceEvents {
		return errors.New("memory provenance must contain 1-64 source events")
	}
	if err := validateUniqueIDs("source_event_ids", document.SourceEventIDs); err != nil {
		return err
	}
	if len(document.Tags) > maxTags {
		return errors.New("memory tags exceed 16 entries")
	}
	return validateUniqueText("tags", document.Tags, 64)
}

func validateMemoryQuery(query MemoryQuery) error {
	if err := validateID("session_id", query.SessionID); err != nil {
		return err
	}
	if err := validateID("actor_id", query.ActorID); err != nil {
		return err
	}
	if err := validateText("text", query.Text, maxQueryTextBytes); err != nil {
		return err
	}
	if query.Limit < 1 || query.Limit > maxMatches {
		return errors.New("memory query limit must be between 1 and 64")
	}
	return nil
}

func validateMemoryMatches(matches []MemoryMatch, limit int) error {
	if len(matches) > limit {
		return errors.New("memory index returned more matches than requested")
	}
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if err := validateID("document_id", match.DocumentID); err != nil {
			return err
		}
		if _, duplicate := seen[match.DocumentID]; duplicate {
			return errors.New("memory index returned a duplicate document")
		}
		seen[match.DocumentID] = struct{}{}
		if math.IsNaN(match.Score) || math.IsInf(match.Score, 0) ||
			match.Score < 0 || match.Score > 1 {
			return errors.New("memory match score must be finite and between zero and one")
		}
	}
	return nil
}

func validateSpeechRequest(request SpeechRequest) error {
	if err := validateIDFields(
		idField{"request_id", request.RequestID},
		idField{"session_id", request.SessionID},
		idField{"actor_id", request.ActorID},
		idField{"operation_id", request.OperationID},
		idField{"voice_id", request.VoiceID},
	); err != nil {
		return err
	}
	if err := validateText("text", request.Text, maxSpeechTextBytes); err != nil {
		return err
	}
	if !hashPattern.MatchString(request.TextSHA256) ||
		request.TextSHA256 != textHash(request.Text) {
		return errors.New("text_sha256 does not match approved text")
	}
	tag, err := language.Parse(request.Language)
	if err != nil || tag == language.Und {
		return errors.New("language must be a concrete BCP 47 tag")
	}
	mediaType, parameters, err := mime.ParseMediaType(request.MediaType)
	if err != nil || len(parameters) != 0 || mediaType != request.MediaType ||
		!strings.HasPrefix(mediaType, "audio/") {
		return errors.New("media_type must be canonical audio without parameters")
	}
	return nil
}

func validateAudioArtifact(request SpeechRequest, artifact AudioArtifactRef) error {
	if err := protocol.ValidateArtifactRef(artifact.Ref); err != nil {
		return err
	}
	if artifact.Ref.MediaType != request.MediaType {
		return errors.New("speech artifact media type changed")
	}
	if artifact.TextSHA256 != request.TextSHA256 {
		return errors.New("speech artifact is not bound to the approved text")
	}
	if artifact.DurationMillis == 0 ||
		artifact.DurationMillis > uint64(protocol.MaxJSONSafeInteger) {
		return errors.New("speech duration must be a positive JSON-safe integer")
	}
	return nil
}

func validateUniqueIDs(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateID(field, value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains a duplicate", field)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUniqueText(field string, values []string, maximum int) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateText(field, value, maximum); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains a duplicate", field)
		}
		seen[value] = struct{}{}
	}
	return nil
}
