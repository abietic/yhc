package filehistory

import "fmt"

type restoreReceiptRequirement struct {
	id, session, operation, path, tool string
	before, after                      restorePlanState
}

func (want restoreReceiptRequirement) matches(record RevisionRecord) bool {
	return record.ID == want.id && record.SessionID == want.session && record.TurnID == want.operation && record.ToolUseID == want.operation && record.ToolName == want.tool && record.Path == want.path && record.Outcome == "succeeded" && record.ParentExisted && record.Sequence > 0 && restoreStateMetadata(record.Before) == want.before && restoreStateMetadata(record.After) == want.after
}

func (j *RevisionJournal) restoreReceiptRequirement(plan restorePlan, direction string, index int) (restoreReceiptRequirement, error) {
	path := plan.Paths[index]
	want := restoreReceiptRequirement{id: fmt.Sprintf("restore-%s-%d-%s", plan.ID, index, direction), session: plan.SessionID, operation: plan.ID, path: path.Path, tool: "WorkspaceRestore", before: path.Original, after: path.Target}
	if direction == "rollback" {
		// Rollback returns to the operation's immutable original observation.
		// Later ordinary writes cannot retroactively change its net transition.
		want.tool, want.before, want.after = "WorkspaceRestoreRollback", path.Original, path.Original
		forwardID := fmt.Sprintf("restore-%s-%d-restore", plan.ID, index)
		if forward, exists := j.records[forwardID]; exists {
			bound := restoreReceiptRequirement{id: forwardID, session: plan.SessionID, operation: plan.ID, path: path.Path, tool: "WorkspaceRestore", before: path.Original, after: path.Target}
			if !bound.matches(forward) {
				return want, fmt.Errorf("restore forward receipt binding mismatch")
			}
			if rollback, found := j.records[want.id]; found && forward.Sequence >= rollback.Sequence {
				return want, fmt.Errorf("restore receipt order mismatch")
			}
			want.before = path.Target
		}
	}
	if actual, found := j.records[want.id]; found && !want.matches(actual) {
		return want, fmt.Errorf("restore receipt binding mismatch")
	}
	return want, nil
}

func (j *RevisionJournal) verifyRestoreReceiptPlan(plan restorePlan, direction string) error {
	for index := range plan.Paths {
		want, err := j.restoreReceiptRequirement(plan, direction, index)
		if err != nil {
			return err
		}
		if _, found := j.records[want.id]; !found && want.before != want.after {
			return fmt.Errorf("restore receipts incomplete")
		}
	}
	return nil
}
