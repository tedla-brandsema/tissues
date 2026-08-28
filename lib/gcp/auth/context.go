package gcp

import "context"

type ctxSubjectKey struct{}
type ctxEmailKey struct{}

// WithSubject stores the authenticated Identity Platform subject in context.
func WithSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, ctxSubjectKey{}, subject)
}

// SubjectFromContext returns the authenticated Identity Platform subject from context.
func SubjectFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(ctxSubjectKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// WithEmail stores the authenticated Identity Platform email in context.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, ctxEmailKey{}, email)
}

// EmailFromContext returns the authenticated Identity Platform email from context.
func EmailFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(ctxEmailKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
