package extension

import (
	"context"
	"errors"
)

func RebuildMemoryIndex(
	ctx context.Context,
	index MemoryIndex,
	sessionID string,
	documents []MemoryDocument,
) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if index == nil {
		return errors.New("memory index is required")
	}
	if err := validateID("session_id", sessionID); err != nil {
		return err
	}
	copied := make([]MemoryDocument, len(documents))
	for position, document := range documents {
		if document.SessionID != sessionID {
			return errors.New("memory document belongs to another Session")
		}
		document.TextSHA256 = textHash(document.Text)
		if err := validateMemoryDocument(document); err != nil {
			return err
		}
		copied[position] = cloneMemoryDocument(document)
	}
	return index.ReplaceSession(ctx, sessionID, copied)
}

func SearchMemory(
	ctx context.Context,
	index MemoryIndex,
	query MemoryQuery,
) ([]MemoryMatch, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if index == nil {
		return nil, errors.New("memory index is required")
	}
	if err := validateMemoryQuery(query); err != nil {
		return nil, err
	}
	matches, err := index.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	if err := validateMemoryMatches(matches, query.Limit); err != nil {
		return nil, err
	}
	return append([]MemoryMatch(nil), matches...), nil
}

func DeleteMemoryIndex(
	ctx context.Context,
	index MemoryIndex,
	sessionID string,
) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if index == nil {
		return errors.New("memory index is required")
	}
	if err := validateID("session_id", sessionID); err != nil {
		return err
	}
	return index.DeleteSession(ctx, sessionID)
}

func cloneMemoryDocument(document MemoryDocument) MemoryDocument {
	document.SourceEventIDs = append([]string(nil), document.SourceEventIDs...)
	document.Tags = append([]string(nil), document.Tags...)
	return document
}
