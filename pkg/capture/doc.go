// Package capture defines the platform-agnostic data models for MockNetPack
// device registration, capture sessions and traffic records.
//
// It intentionally has no dependencies on other mockd packages (pkg/store,
// pkg/admin, ...) so that both the persistence layer and the API layer can
// import it without import cycles, and so the models stay platform-neutral
// (no iOS/Android-specific types) per requirement G5.
package capture
