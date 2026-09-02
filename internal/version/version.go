package version

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("bareplane %s (commit=%s, built=%s)", Version, Commit, Date)
}
