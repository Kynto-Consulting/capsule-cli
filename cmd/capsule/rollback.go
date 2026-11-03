package main

import "sort"

// pickRollbackTarget chooses the deployment `capsule deployments rollback`
// redeploys. items may come in any order; they are ordered newest first.
//
//   - The deployment running now is the newest "success" (a failed or running
//     deployment does not replace it).
//   - If the most recent deployment succeeded, it is the current one: the
//     target is the last "success" before it.
//   - If the most recent deployment failed (or was cancelled), the target is
//     the last "success": the known-good version.
//   - If the most recent deployment is still in progress, the current one is
//     the newest success and the target is the success before that one.
//
// It returns nil when there is nothing to roll back to.
func pickRollbackTarget(items []deploymentsAPIItem) *deploymentsAPIItem {
	sorted := make([]deploymentsAPIItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt > sorted[j].CreatedAt })
	cur := -1
	for i := range sorted {
		if sorted[i].Status == "success" {
			cur = i
			break
		}
	}
	if cur < 0 {
		return nil
	}
	switch sorted[0].Status {
	case "failed", "failure", "error", "cancelled", "canceled":
		return &sorted[cur] // the newest attempt failed: go back to the last good one
	}
	for i := cur + 1; i < len(sorted); i++ {
		if sorted[i].Status == "success" {
			return &sorted[i]
		}
	}
	return nil
}
