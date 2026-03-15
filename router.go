package main

import "fmt"

func BuildReviewTask(source *Task, reviewBy string, maxRetries int) *Task {
	review := NewTask(CreateTaskRequest{
		Title:       fmt.Sprintf("Review: %s", source.Title),
		Description: fmt.Sprintf("Review the output of task %q.\n\nOriginal summary:\n%s", source.Title, source.Result),
		AssignedTo:  reviewBy,
		DependsOn:   []string{source.ID},
	}, maxRetries)
	review.ParentID = source.ID
	review.IsReviewTask = true
	return review
}

func BuildReviewActionTask(source, review *Task, maxRetries int) *Task {
	followUp := NewTask(CreateTaskRequest{
		Title: fmt.Sprintf("Address review: %s", source.Title),
		Description: fmt.Sprintf(
			"Implement the review feedback for task %q.\n\nOriginal task:\n%s\n\nOriginal result:\n%s\n\nReview feedback:\n%s\n\nMake the necessary improvements, then summarize exactly what you changed.",
			source.Title,
			source.Description,
			source.Result,
			review.Result,
		),
		AssignedTo: source.AssignedTo,
		DependsOn:  []string{review.ID},
	}, maxRetries)
	followUp.ParentID = source.ID
	return followUp
}
