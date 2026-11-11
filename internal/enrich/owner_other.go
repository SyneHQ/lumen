//go:build !unix

package enrich

import "os"

// ponytail: no uid ownership check off unix (Windows uses ACLs); mode bits still checked.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }
