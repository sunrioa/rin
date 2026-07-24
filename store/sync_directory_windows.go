//go:build windows

package store

func syncDirectory(path string) error {
	// Windows documents directory handles for metadata queries and change
	// notifications, but not for FlushFileBuffers. Durable file publication
	// therefore uses MoveFileExW with MOVEFILE_WRITE_THROUGH in
	// renameDurably. There is no documented directory-fsync equivalent to
	// perform here.
	return nil
}
