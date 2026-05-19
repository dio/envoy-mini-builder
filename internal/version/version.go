package version

// These are injected by goreleaser ldflags at release time.
// Running from source gives "dev".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
