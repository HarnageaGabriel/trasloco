//go:build !unix

package cli

import "os"

// Portable in-place writes are not atomic. Keep replacement on Windows;
// explicit ACLs and alternate data streams are not copied (see --help).
func preserveModernizeOwner(_ *os.File, _ os.FileInfo) error { return nil }

func syncModernizeDirectory(_ *os.Root) error { return nil }
