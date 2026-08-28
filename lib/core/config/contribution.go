package config

// Contribution marks a typed struct as a service configuration contribution.
// Embed it in a service's Config type. It carries no configuration state.
type Contribution struct{}

func (Contribution) serviceConfigContribution() {}

// ServiceContribution is the compile-time contract for service configuration
// types. Embedding Contribution satisfies it without application boilerplate.
type ServiceContribution interface {
	serviceConfigContribution()
}

// NewServiceProfile validates a resolved service configuration contribution
// and creates its initial immutable profile snapshot.
func NewServiceProfile[T ServiceContribution](name string, value T) (Profile[T], error) {
	return NewProfile(name, value)
}
