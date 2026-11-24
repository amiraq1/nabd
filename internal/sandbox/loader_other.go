//go:build !linux || android

package sandbox

// LoaderBinds reports no host loader files outside Linux. The isolation
// backends only exist on Linux, so no caller reaches a bind list elsewhere.
func LoaderBinds(string) ([]string, error) { return nil, nil }
