package workboard

import (
	"fmt"
	"reflect"
	"unicode/utf8"
)

const (
	maxProjectionDiagnostics     = 128
	maxProjectionDiagnosticRunes = 512
)

// ProjectionDiagnostic records a rejected process-local projection update.
// It is deliberately not part of the durable WorkBoard schema.
type ProjectionDiagnostic struct {
	Sequence uint64
	Kind     string
	Message  string
}

// ProjectionSnapshot is a defensive, process-local view of the current
// authoritative WorkBoard record and rejected projection updates.
type ProjectionSnapshot struct {
	Available   bool
	SessionID   string
	BoardID     string
	Revision    uint64
	Record      AuthorityRecord
	Diagnostics []ProjectionDiagnostic
}

// ProjectionReducer serializes the in-process projection of the durable
// authority record. Its callers hold the adapter mutation lock.
type ProjectionReducer struct {
	record      *AuthorityRecord
	diagnostics []ProjectionDiagnostic
	sequence    uint64
}

type projectionReservation struct {
	reducer *ProjectionReducer
	next    AuthorityRecord
	done    bool
}

func NewProjectionReducer() *ProjectionReducer {
	return &ProjectionReducer{}
}

func (r *ProjectionReducer) clone() *ProjectionReducer {
	if r == nil {
		return NewProjectionReducer()
	}
	cloned := &ProjectionReducer{
		diagnostics: append([]ProjectionDiagnostic(nil), r.diagnostics...),
		sequence:    r.sequence,
	}
	if r.record != nil {
		record := cloneAuthorityRecord(*r.record)
		cloned.record = &record
	}
	return cloned
}

func (r *ProjectionReducer) Bootstrap(record AuthorityRecord) error {
	if err := validateAuthorityRecord(record, record.SessionID); err != nil {
		return err
	}
	if r.record == nil {
		copy := cloneAuthorityRecord(record)
		r.record = &copy
		return nil
	}
	current := *r.record
	if current.BoardID != record.BoardID {
		r.diagnose("projection bootstrap BoardID mismatch")
		return fmt.Errorf("workboard projection: bootstrap BoardID mismatch")
	}
	if record.Board.Revision < current.Board.Revision {
		r.diagnose("projection bootstrap revision regression")
		return fmt.Errorf("workboard projection: bootstrap revision regression")
	}
	if record.Board.Revision == current.Board.Revision {
		if !reflect.DeepEqual(
			cloneAuthorityRecord(current),
			cloneAuthorityRecord(record),
		) {
			r.diagnose("projection bootstrap same-revision content mismatch")
			return fmt.Errorf("workboard projection: bootstrap content mismatch")
		}
		return nil
	}
	copy := cloneAuthorityRecord(record)
	r.record = &copy
	return nil
}

func (r *ProjectionReducer) reserve(
	expectedBoardID string,
	expectedRevision uint64,
	next AuthorityRecord,
) (*projectionReservation, error) {
	if r.record == nil {
		r.diagnose("projection reservation before bootstrap")
		return nil, fmt.Errorf("workboard projection: reservation before bootstrap")
	}
	current := *r.record
	if current.BoardID != expectedBoardID ||
		current.Board.Revision != expectedRevision {
		r.diagnose("projection reservation identity or revision mismatch")
		return nil, fmt.Errorf("workboard projection: reservation identity or revision mismatch")
	}
	if next.BoardID != expectedBoardID ||
		next.Board.Revision != expectedRevision+1 {
		r.diagnose("projection reservation invalid next identity or revision")
		return nil, fmt.Errorf("workboard projection: reservation invalid next identity or revision")
	}
	if err := validateAuthorityRecord(next, next.SessionID); err != nil {
		r.diagnose("projection reservation invalid authority record")
		return nil, err
	}
	return &projectionReservation{reducer: r, next: cloneAuthorityRecord(next)}, nil
}

func (r *ProjectionReducer) Snapshot() ProjectionSnapshot {
	snapshot := ProjectionSnapshot{
		Diagnostics: append([]ProjectionDiagnostic(nil), r.diagnostics...),
	}
	if r.record != nil {
		snapshot.Record = cloneAuthorityRecord(*r.record)
		snapshot.Available = true
		snapshot.SessionID = snapshot.Record.SessionID
		snapshot.BoardID = snapshot.Record.BoardID
		snapshot.Revision = snapshot.Record.Board.Revision
	}
	return snapshot
}

func (r *ProjectionReducer) diagnose(message string) {
	r.sequence++
	if len(r.diagnostics) == maxProjectionDiagnostics {
		copy(r.diagnostics, r.diagnostics[1:])
		r.diagnostics = r.diagnostics[:maxProjectionDiagnostics-1]
	}
	r.diagnostics = append(r.diagnostics, ProjectionDiagnostic{
		Sequence: r.sequence,
		Kind:     "rejected_update",
		Message:  truncateProjectionDiagnostic(message),
	})
}

func (r *projectionReservation) Commit() {
	if r == nil || r.done {
		return
	}
	copy := cloneAuthorityRecord(r.next)
	r.reducer.record = &copy
	r.done = true
}

func (r *projectionReservation) Abort() {
	if r != nil {
		r.done = true
	}
}

func truncateProjectionDiagnostic(message string) string {
	if utf8.RuneCountInString(message) <= maxProjectionDiagnosticRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxProjectionDiagnosticRunes])
}
