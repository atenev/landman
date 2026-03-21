package controllers

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// timeEqual returns true when both metav1.Time pointers represent the same instant
// (or are both nil).
func timeEqual(a, b *metav1.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Time.Equal(b.Time)
}
