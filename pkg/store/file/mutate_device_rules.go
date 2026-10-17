package file

import (
	"github.com/getmockd/mockd/pkg/store"
)

// mutateDeviceRules runs fn over the device-scoped subset of all, persists the
// result and bumps the device's rule-set version — all under one FileStore
// lock, so the rule set and its version can never be observed half-updated
// (4.9; 对齐 push mock 规则集). fn receives a fresh slice of the device's
// current rules (safe to append/reorder/remove) and returns the new set;
// changed=false skips the bump and returns the current version; any error
// aborts with the store untouched. appOf/didOf read the device scope off each
// rule record. The returned int is the rule-set version after the call.
func mutateDeviceRules[T any](
	fs *FileStore,
	all *[]*T,
	versions *map[string]int,
	app, did string,
	fn func([]*T) ([]*T, bool, error),
	appOf, didOf func(*T) string,
) (int, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.cfg.ReadOnly {
		return 0, store.ErrReadOnly
	}

	deviceRules := make([]*T, 0, len(*all))
	for _, r := range *all {
		if appOf(r) == app && didOf(r) == did {
			deviceRules = append(deviceRules, r)
		}
	}
	updated, changed, err := fn(deviceRules)
	if err != nil {
		return 0, err
	}
	if !changed {
		return (*versions)[versionKey(app, did)], nil
	}

	// Replace this device's rules in place, preserving every other device's rows.
	out := make([]*T, 0, len(*all)-len(deviceRules)+len(updated))
	for _, r := range *all {
		if appOf(r) == app && didOf(r) == did {
			continue
		}
		out = append(out, r)
	}
	out = append(out, updated...)
	*all = out

	k := versionKey(app, did)
	v := (*versions)[k] + 1
	if *versions == nil {
		*versions = make(map[string]int)
	}
	(*versions)[k] = v
	fs.markDirty()
	return v, nil
}
