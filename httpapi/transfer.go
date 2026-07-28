package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

const transferMediaType = "application/x-ndjson"

var errTransferFrameTooLarge = errors.New("transfer frame exceeds its byte limit")

func (s *Server) exportSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	sink := &httpTransferSink{response: response}
	err := s.engine.ExportTransfer(request.Context(), input, sink)
	if err == nil {
		return
	}
	if !sink.started {
		s.respond(response, request, nil, err)
		return
	}
	if writeErr := sink.writeError(err); writeErr != nil {
		s.logger.Debug("transfer export ended after client disconnect", "error", writeErr)
	}
}

func (s *Server) importSession(response http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != transferMediaType {
		s.writeError(
			response,
			http.StatusUnsupportedMediaType,
			"invalid_content_type",
			"Content-Type must be application/x-ndjson",
			"",
		)
		return
	}
	encoding := strings.TrimSpace(request.Header.Get("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		s.writeError(
			response,
			http.StatusUnsupportedMediaType,
			"unsupported_content_encoding",
			"compressed transfer request bodies are not supported",
			"",
		)
		return
	}
	expectedBinding := protocol.Binding{
		GameID:         request.Header.Get("Rin-Expected-Game-Id"),
		ContentID:      request.Header.Get("Rin-Expected-Content-Id"),
		ContentVersion: request.Header.Get("Rin-Expected-Content-Version"),
		ContentHash:    request.Header.Get("Rin-Expected-Content-Hash"),
	}
	if err := protocol.ValidateBinding(expectedBinding); err != nil {
		s.respondTransferValidation(response, err)
		return
	}

	reader := bufio.NewReaderSize(request.Body, 64*1024)
	manifestLine, manifestBytes, err := readTransferLine(
		reader,
		protocol.TransferControlFrameMaxBytes,
	)
	if err != nil {
		s.respondTransferReadError(response, err, "manifest")
		return
	}
	transferredBytes := manifestBytes
	if transferredBytes > s.maxTransferBytes {
		s.respond(response, request, nil, transferTooLargeError())
		return
	}
	var manifest protocol.TransferManifest
	if err := decodeTransferFrame(manifestLine, protocol.TransferFrameManifest, &manifest); err != nil {
		s.respondTransferReadError(response, err, "manifest")
		return
	}
	if err := protocol.ValidateTransferManifest(manifest); err != nil {
		s.respondTransferValidation(response, err)
		return
	}
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		s.respondTransferValidation(response, err)
		return
	}
	writer, err := s.engine.BeginTransferImport(manifest, expectedBinding)
	if err != nil {
		s.respond(response, request, nil, err)
		return
	}
	defer func() {
		if err := writer.Abort(); err != nil {
			s.logger.Error("abort transfer import", "error", err)
		}
	}()

	for eventIndex := uint64(0); eventIndex < manifest.EventCount; eventIndex++ {
		if err := request.Context().Err(); err != nil {
			s.respond(
				response,
				request,
				nil,
				rinruntime.NewError(
					"transfer_cancelled",
					"transfer was cancelled",
					err,
				),
			)
			return
		}
		line, lineBytes, err := readTransferLine(
			reader,
			protocol.TransferEventFrameMaxBytes,
		)
		if err != nil {
			s.respondTransferReadError(response, err, "event")
			return
		}
		if exceedsTransferWireBytes(
			transferredBytes,
			lineBytes,
			s.maxTransferBytes,
		) {
			s.respond(response, request, nil, transferTooLargeError())
			return
		}
		transferredBytes += lineBytes
		var frame protocol.TransferEvent
		if err := decodeTransferFrame(line, protocol.TransferFrameEvent, &frame); err != nil {
			s.respondTransferReadError(response, err, "event")
			return
		}
		if err := hasher.WriteEvent(frame); err != nil {
			s.respondTransferValidation(response, err)
			return
		}
		if err := writer.WriteEvent(frame); err != nil {
			s.respond(response, request, nil, err)
			return
		}
	}

	completeLine, completeBytes, err := readTransferLine(
		reader,
		protocol.TransferControlFrameMaxBytes,
	)
	if err != nil {
		s.respondTransferReadError(response, err, "complete")
		return
	}
	if exceedsTransferWireBytes(
		transferredBytes,
		completeBytes,
		s.maxTransferBytes,
	) {
		s.respond(response, request, nil, transferTooLargeError())
		return
	}
	transferredBytes += completeBytes
	var complete protocol.TransferComplete
	if err := decodeTransferFrame(completeLine, protocol.TransferFrameComplete, &complete); err != nil {
		s.respondTransferReadError(response, err, "complete")
		return
	}
	if err := hasher.VerifyComplete(complete, manifest); err != nil {
		s.respondTransferValidation(response, err)
		return
	}
	if trailing, trailingBytes, err := readTransferLine(
		reader,
		protocol.TransferControlFrameMaxBytes,
	); err != io.EOF {
		if exceedsTransferWireBytes(
			transferredBytes,
			trailingBytes,
			s.maxTransferBytes,
		) {
			s.respond(response, request, nil, transferTooLargeError())
			return
		}
		if err == nil && len(trailing) > 0 {
			err = errors.New("transfer contains a frame after complete")
		}
		s.respondTransferReadError(response, err, "complete")
		return
	}
	if err := writer.Publish(complete); err != nil {
		s.respond(response, request, nil, err)
		return
	}
	s.respond(response, request, protocol.MutationResult{
		SessionID: manifest.SessionID,
		Revision:  manifest.TerminalRevision,
		HeadHash:  manifest.TerminalHeadHash,
	}, nil)
}

func (s *Server) respondTransferValidation(
	response http.ResponseWriter,
	err error,
) {
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		s.writeError(
			response,
			http.StatusBadRequest,
			"invalid_transfer",
			validation.Message,
			validation.Field,
		)
		return
	}
	s.writeError(
		response,
		http.StatusBadRequest,
		"invalid_transfer",
		"transfer metadata is invalid",
		"",
	)
}

func (s *Server) respondTransferReadError(
	response http.ResponseWriter,
	err error,
	field string,
) {
	if errors.Is(err, errTransferFrameTooLarge) {
		s.writeError(
			response,
			http.StatusRequestEntityTooLarge,
			"transfer_frame_too_large",
			err.Error(),
			field,
		)
		return
	}
	s.writeError(
		response,
		http.StatusBadRequest,
		"invalid_transfer",
		"transfer contains an invalid "+field+" frame",
		field,
	)
}

func readTransferLine(
	reader *bufio.Reader,
	limit int,
) ([]byte, uint64, error) {
	var line []byte
	var consumed uint64
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += uint64(len(fragment))
		if len(line)+len(fragment) > limit+1 {
			return nil, consumed, fmt.Errorf(
				"%w: maximum is %d bytes",
				errTransferFrameTooLarge,
				limit,
			)
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				return nil, consumed, errors.New(
					"transfer frame must not be empty",
				)
			}
			return line, consumed, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) == 0:
			return nil, consumed, io.EOF
		case errors.Is(err, io.EOF):
			return nil, consumed, errors.New(
				"transfer frame must end with LF",
			)
		default:
			return nil, consumed, err
		}
	}
}

func exceedsTransferWireBytes(current, additional, maximum uint64) bool {
	return maximum == 0 ||
		additional > maximum ||
		current > maximum-additional
}

func transferTooLargeError() error {
	return rinruntime.NewError(
		"transfer_too_large",
		"Transfer exceeds the configured byte limit",
		rinruntime.ErrConflict,
	)
}

func decodeTransferFrame(line []byte, expectedType string, target any) error {
	if !jsonwire.Valid(line) {
		return errors.New("transfer frame must be valid UTF-8 JSON")
	}
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &discriminator); err != nil {
		return err
	}
	if discriminator.Type != expectedType {
		return fmt.Errorf("transfer frame type is %q, want %q", discriminator.Type, expectedType)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("transfer frame must contain exactly one JSON object")
	}
	return nil
}

type httpTransferSink struct {
	response http.ResponseWriter
	started  bool
	finished bool
}

var _ rinruntime.TransferSink = (*httpTransferSink)(nil)

func (s *httpTransferSink) WriteManifest(frame protocol.TransferManifest) error {
	if s.started {
		return errors.New("transfer response has already started")
	}
	s.response.Header().Set("Content-Type", transferMediaType+"; charset=utf-8")
	s.response.WriteHeader(http.StatusOK)
	s.started = true
	return s.writeFrame(frame, protocol.TransferControlFrameMaxBytes)
}

func (s *httpTransferSink) WriteEvent(frame protocol.TransferEvent) error {
	if !s.started || s.finished {
		return errors.New("transfer response is not writable")
	}
	return s.writeFrame(frame, protocol.TransferEventFrameMaxBytes)
}

func (s *httpTransferSink) WriteComplete(frame protocol.TransferComplete) error {
	if !s.started || s.finished {
		return errors.New("transfer response is not writable")
	}
	if err := s.writeFrame(frame, protocol.TransferControlFrameMaxBytes); err != nil {
		return err
	}
	s.finished = true
	return nil
}

func (s *httpTransferSink) writeError(err error) error {
	if s.finished {
		return nil
	}
	detail := protocol.NewErrorDetail(
		rinruntime.ErrorCode(err),
		err.Error(),
		rinruntime.ErrorField(err),
	)
	frame := protocol.TransferError{
		Type:  protocol.TransferFrameError,
		Error: *detail,
	}
	if validationErr := protocol.ValidateTransferError(frame); validationErr != nil {
		return validationErr
	}
	writeErr := s.writeFrame(frame, protocol.TransferControlFrameMaxBytes)
	s.finished = true
	return writeErr
}

func (s *httpTransferSink) writeFrame(frame any, limit int) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(payload) > limit {
		return fmt.Errorf("%w: maximum is %d bytes", errTransferFrameTooLarge, limit)
	}
	payload = append(payload, '\n')
	if _, err := s.response.Write(payload); err != nil {
		return err
	}
	if flusher, ok := s.response.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
