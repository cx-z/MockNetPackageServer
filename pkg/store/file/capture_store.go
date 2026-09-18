package file

import (
	"context"

	"github.com/getmockd/mockd/pkg/capture"
	"github.com/getmockd/mockd/pkg/store"
)

// deviceStore implements store.DeviceStore using the FileStore's in-memory
// data + debounced persistence (same pattern as requestLogStore).
type deviceStore struct {
	fs *FileStore
}

// List returns all devices matching the filter ("" app = no filter).
func (s *deviceStore) List(ctx context.Context, filter *store.DeviceFilter) ([]*capture.Device, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	result := make([]*capture.Device, 0, len(s.fs.data.Devices))
	for _, d := range s.fs.data.Devices {
		if filter != nil && filter.App != "" && d.App != filter.App {
			continue
		}
		result = append(result, d)
	}
	return result, nil
}

// Get returns a device by (App, Did).
func (s *deviceStore) Get(ctx context.Context, app, did string) (*capture.Device, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, d := range s.fs.data.Devices {
		if d.App == app && d.Did == did {
			return d, nil
		}
	}
	return nil, store.ErrNotFound
}

// Create adds a new device. (App, Did) must be unique.
func (s *deviceStore) Create(ctx context.Context, d *capture.Device) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.Devices {
		if existing.App == d.App && existing.Did == d.Did {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.Devices = append(s.fs.data.Devices, d)
	s.fs.markDirty()
	return nil
}

// Update replaces an existing device.
func (s *deviceStore) Update(ctx context.Context, d *capture.Device) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, existing := range s.fs.data.Devices {
		if existing.App == d.App && existing.Did == d.Did {
			s.fs.data.Devices[i] = d
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Delete removes a device by (App, Did).
func (s *deviceStore) Delete(ctx context.Context, app, did string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, d := range s.fs.data.Devices {
		if d.App == app && d.Did == did {
			s.fs.data.Devices = append(s.fs.data.Devices[:i], s.fs.data.Devices[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Count returns the total number of devices.
func (s *deviceStore) Count(ctx context.Context) (int, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()
	return len(s.fs.data.Devices), nil
}

// captureSessionStore implements store.CaptureSessionStore using the
// FileStore's in-memory data + debounced persistence.
type captureSessionStore struct {
	fs *FileStore
}

// List returns sessions matching the filter, most recent first.
func (s *captureSessionStore) List(ctx context.Context, filter *store.SessionFilter) ([]*capture.CaptureSession, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	result := make([]*capture.CaptureSession, 0, len(s.fs.data.CaptureSessions))
	for _, sess := range s.fs.data.CaptureSessions {
		if filter != nil {
			if filter.App != nil && sess.App != *filter.App {
				continue
			}
			if filter.Did != nil && sess.Did != *filter.Did {
				continue
			}
			if filter.Status != nil && sess.Status != *filter.Status {
				continue
			}
		}
		result = append(result, sess)
	}

	// Most recent first (stable: keep relative order within equal startedAt).
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].StartedAt.After(result[j-1].StartedAt); j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result, nil
}

// Get returns a session by ID.
func (s *captureSessionStore) Get(ctx context.Context, id string) (*capture.CaptureSession, error) {
	s.fs.mu.RLock()
	defer s.fs.mu.RUnlock()

	for _, sess := range s.fs.data.CaptureSessions {
		if sess.ID == id {
			return sess, nil
		}
	}
	return nil, store.ErrNotFound
}

// Create adds a new session. The ID must be unique.
func (s *captureSessionStore) Create(ctx context.Context, sess *capture.CaptureSession) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for _, existing := range s.fs.data.CaptureSessions {
		if existing.ID == sess.ID {
			return store.ErrAlreadyExists
		}
	}

	s.fs.data.CaptureSessions = append(s.fs.data.CaptureSessions, sess)
	s.fs.markDirty()
	return nil
}

// Update replaces an existing session.
func (s *captureSessionStore) Update(ctx context.Context, sess *capture.CaptureSession) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, existing := range s.fs.data.CaptureSessions {
		if existing.ID == sess.ID {
			s.fs.data.CaptureSessions[i] = sess
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}

// Delete removes a session by ID.
func (s *captureSessionStore) Delete(ctx context.Context, id string) error {
	s.fs.mu.Lock()
	defer s.fs.mu.Unlock()
	if s.fs.cfg.ReadOnly {
		return store.ErrReadOnly
	}

	for i, sess := range s.fs.data.CaptureSessions {
		if sess.ID == id {
			s.fs.data.CaptureSessions = append(s.fs.data.CaptureSessions[:i], s.fs.data.CaptureSessions[i+1:]...)
			s.fs.markDirty()
			return nil
		}
	}
	return store.ErrNotFound
}
