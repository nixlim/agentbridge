package main

import "testing"

func TestBuildReviewTask(t *testing.T) {
	t.Parallel()

	source := NewTask(CreateTaskRequest{
		Title:       "Implement auth",
		Description: "Add login flow",
		AssignedTo:  "claude",
	}, 2)
	source.Result = "Implemented login flow"

	review := BuildReviewTask(source, "codex", 3)
	if review.AssignedTo != "codex" {
		t.Fatalf("expected review assigned to codex, got %s", review.AssignedTo)
	}
	if !review.IsReviewTask {
		t.Fatal("expected review task flag to be true")
	}
	if review.ParentID != source.ID {
		t.Fatalf("expected parent %s, got %s", source.ID, review.ParentID)
	}
	if len(review.DependsOn) != 1 || review.DependsOn[0] != source.ID {
		t.Fatalf("unexpected dependencies %#v", review.DependsOn)
	}
}
