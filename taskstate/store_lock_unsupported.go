//go:build !windows && !((darwin && !ios) || (linux && !android))

package taskstate

import "os"

func acquireStoreLock(path string) (*os.File, error) { return openStoreLock(path) }
func releaseStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
