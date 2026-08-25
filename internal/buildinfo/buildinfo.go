// Package buildinfo exposes immutable metadata injected into release binaries.
package buildinfo

// These values are replaced with -ldflags during a release build.
var (
	Version   = "0.0.0"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info is the build identity presented by CLI and API boundaries.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

// Current returns the metadata compiled into the running binary.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
	}
}
